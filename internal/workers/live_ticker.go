package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/kadi/backend/internal/config"
	"github.com/kadi/backend/internal/db/queries"
)

// LiveTickerWorker polls the external sports API every ~12 seconds
// for live match updates (scores, minute, status changes).
// When a match finishes, it triggers the bet settlement pipeline.
type LiveTickerWorker struct {
	cfg      *config.Config
	fixtures *queries.FixtureStore
	bankroll *queries.BankrollStore
	client   *http.Client

	// Track which fixture IDs were live on the last tick to detect transitions
	previouslyLive map[string]string // fixtureAPIID -> previous status
}

// NewLiveTickerWorker constructs a LiveTickerWorker.
func NewLiveTickerWorker(
	cfg *config.Config,
	fixtures *queries.FixtureStore,
	bankroll *queries.BankrollStore,
) *LiveTickerWorker {
	return &LiveTickerWorker{
		cfg:      cfg,
		fixtures: fixtures,
		bankroll: bankroll,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		previouslyLive: make(map[string]string),
	}
}

// Run starts the live ticker loop. Exits when ctx is cancelled.
func (w *LiveTickerWorker) Run(ctx context.Context) {
	interval := time.Duration(w.cfg.LiveTickerIntervalSec) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Printf("[live-ticker] worker started — polling every %s", interval)

	for {
		select {
		case <-ctx.Done():
			log.Println("[live-ticker] worker stopped")
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

// tick performs one polling cycle.
func (w *LiveTickerWorker) tick(ctx context.Context) {
	liveFixtures, err := w.fetchLiveFixtures(ctx)
	if err != nil {
		log.Printf("[live-ticker] ERROR fetching live fixtures: %v", err)
		return
	}

	if len(liveFixtures) == 0 {
		// Check if any previously-live fixtures have finished
		w.detectFinishedMatches(ctx)
		return
	}

	for _, f := range liveFixtures {
		apiID := fmt.Sprintf("apf-%d", f.Fixture.ID)
		status := mapStatus(f.Fixture.Status.Short)
		homeScore := derefInt(f.Goals.Home)
		awayScore := derefInt(f.Goals.Away)
		minute := derefInt(f.Fixture.Status.Elapsed)

		// Persist the live update — Supabase Realtime picks this up
		// and broadcasts it directly to connected Next.js clients
		if err := w.fixtures.UpdateLiveScore(ctx, apiID, status, homeScore, awayScore, minute); err != nil {
			log.Printf("[live-ticker] ERROR updating %s: %v", apiID, err)
			continue
		}

		w.previouslyLive[apiID] = status

		if status == "finished" {
			log.Printf("[live-ticker] match FINISHED: %s — triggering settlement", apiID)
			go w.settleMatchBets(ctx, apiID, homeScore, awayScore)
		}
	}
}

// detectFinishedMatches checks if any fixture that was live on the previous tick
// has now transitioned to finished (i.e., it no longer appears in the live feed).
func (w *LiveTickerWorker) detectFinishedMatches(ctx context.Context) {
	for apiID, prevStatus := range w.previouslyLive {
		if prevStatus == "live" {
			// It disappeared from the live feed — assume it finished
			log.Printf("[live-ticker] %s no longer live — marking finished", apiID)
			if err := w.fixtures.UpdateLiveScore(ctx, apiID, "finished", 0, 0, 90); err != nil {
				log.Printf("[live-ticker] ERROR marking %s finished: %v", apiID, err)
			}
			delete(w.previouslyLive, apiID)
		}
	}
}

// settleMatchBets is called in a goroutine when a match finishes.
// It finds all pending bet slips that contain this fixture and settles them.
func (w *LiveTickerWorker) settleMatchBets(ctx context.Context, apiID string, homeScore, awayScore int) {
	// Resolve the fixture UUID from the api_id
	fixtures, err := w.fixtures.ListLive(ctx)
	if err != nil || len(fixtures) == 0 {
		return
	}

	var fixtureID string
	for _, f := range fixtures {
		if f.ApiID == apiID {
			fixtureID = f.ID
			break
		}
	}
	if fixtureID == "" {
		return
	}

	// Determine actual result
	var resultSelection string
	switch {
	case homeScore > awayScore:
		resultSelection = "home_win"
	case awayScore > homeScore:
		resultSelection = "away_win"
	default:
		resultSelection = "draw"
	}

	// Find all pending slips for this fixture
	slips, err := w.bankroll.GetPendingSlipsForFixture(ctx, fixtureID)
	if err != nil {
		log.Printf("[settle] ERROR getting pending slips for %s: %v", fixtureID, err)
		return
	}

	log.Printf("[settle] settling %d pending slip(s) for fixture %s", len(slips), fixtureID)

	for _, slip := range slips {
		// Simple settlement: check if this slip's selection matches
		// (In production, each leg is checked independently for accumulators)
		outcome := "lost"
		returnAmount := 0.0

		for _, leg := range slip.Legs {
			if leg.FixtureID == fixtureID && leg.Selection == resultSelection {
				outcome = "won"
				returnAmount = slip.PotentialReturn
				break
			}
		}

		if err := w.bankroll.SettleBet(ctx, slip.ID, slip.UserID, outcome, returnAmount); err != nil {
			log.Printf("[settle] ERROR settling slip %s: %v", slip.ID, err)
		} else {
			log.Printf("[settle] slip %s settled as %s (return: %.2f)", slip.ID, outcome, returnAmount)
		}
	}
}

// fetchLiveFixtures calls the API-Football /fixtures?live=all endpoint.
func (w *LiveTickerWorker) fetchLiveFixtures(ctx context.Context) ([]APIFootballFixture, error) {
	if w.cfg.APIFootballKey == "" {
		return nil, nil // dev mode: no external fetch
	}

	url := w.cfg.APIFootballURL + "/fixtures?live=all"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-rapidapi-key", w.cfg.APIFootballKey)
	req.Header.Set("x-rapidapi-host", "v3.football.api-sports.io")

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("live request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Response []APIFootballFixture `json:"response"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing live response: %w", err)
	}

	return result.Response, nil
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
