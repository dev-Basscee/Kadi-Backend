package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/kadi/backend/internal/config"
	"github.com/kadi/backend/internal/db/queries"
)

// DailySyncWorker is responsible for syncing fixtures.
type DailySyncWorker struct {
	cfg      *config.Config
	fixtures *queries.FixtureStore
}

// NewDailySyncWorker constructs a DailySyncWorker.
func NewDailySyncWorker(cfg *config.Config, fixtures *queries.FixtureStore) *DailySyncWorker {
	return &DailySyncWorker{
		cfg:      cfg,
		fixtures: fixtures,
	}
}

// Run starts the worker and fetches fixtures from TxLine API once per day (or on startup).
func (w *DailySyncWorker) Run(ctx context.Context) {
	log.Printf("[daily-sync] worker started")
	
	// Run immediately on startup
	w.syncFixtures(ctx)

	// Then run daily
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[daily-sync] worker stopped")
			return
		case <-ticker.C:
			w.syncFixtures(ctx)
		}
	}
}

// TxLineFixture represents a single fixture from the snapshot API
type TxLineFixture struct {
	FixtureID   int         `json:"FixtureId"`
	FixtureID2  int         `json:"fixtureId"`
	HomeTeam    string      `json:"HomeTeam"`
	HomeTeam2   string      `json:"homeTeam"`
	AwayTeam    string      `json:"AwayTeam"`
	AwayTeam2   string      `json:"awayTeam"`
	KickoffTime interface{} `json:"StartTime"`
	KickoffTime2 interface{} `json:"kickoffTime"`
	Status      string      `json:"Status"`
	Status2     string      `json:"status"`
}

func (f *TxLineFixture) GetID() string {
	if f.FixtureID != 0 {
		return fmt.Sprintf("%d", f.FixtureID)
	}
	return fmt.Sprintf("%d", f.FixtureID2)
}

func (f *TxLineFixture) GetHome() string {
	if f.HomeTeam != "" { return f.HomeTeam }
	return f.HomeTeam2
}

func (f *TxLineFixture) GetAway() string {
	if f.AwayTeam != "" { return f.AwayTeam }
	return f.AwayTeam2
}

func (f *TxLineFixture) GetStatus() string {
	s := f.Status
	if s == "" {
		s = f.Status2
	}
	s = strings.ToLower(s)
	switch s {
	case "notstarted", "prematch", "scheduled", "upcoming":
		return "upcoming"
	case "inprogress", "live", "playing", "halftime", "in-play":
		return "live"
	case "finished", "ended", "ft":
		return "finished"
	case "cancelled":
		return "cancelled"
	case "postponed":
		return "postponed"
	default:
		return "upcoming"
	}
}

func (w *DailySyncWorker) syncFixtures(ctx context.Context) {
	log.Println("[daily-sync] Syncing fixtures from TxLine...")
	
	// 1. Get Guest JWT
	jwt, err := fetchGuestJWT(ctx, w.cfg.TxLineStreamURL)
	if err != nil {
		log.Printf("[daily-sync] ERROR fetching guest JWT: %v", err)
		return
	}

	// 2. Derive base URL
	baseURL := w.cfg.TxLineStreamURL
	if idx := strings.Index(baseURL, "/api/"); idx != -1 {
		baseURL = baseURL[:idx]
	}

	// 3. Fetch fixtures snapshot (no competitionId filter so we get all authorized matches)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/fixtures/snapshot", nil)
	if err != nil {
		log.Printf("[daily-sync] ERROR creating request: %v", err)
		return
	}
	
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("X-Api-Token", w.cfg.TxLineAPIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[daily-sync] ERROR fetching fixtures snapshot: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[daily-sync] TxLine returned %d: %s", resp.StatusCode, string(body))
		return
	}

	var txFixtures []TxLineFixture
	if err := json.NewDecoder(resp.Body).Decode(&txFixtures); err != nil {
		log.Printf("[daily-sync] ERROR decoding fixtures snapshot: %v", err)
		return
	}

	log.Printf("[daily-sync] Fetched %d fixtures from TxLine", len(txFixtures))

	for _, txF := range txFixtures {
		apiID := txF.GetID()
		if apiID == "0" {
			continue // Skip invalid fixtures
		}

		f := &queries.Fixture{
			ApiID:        apiID,
			Sport:        "football",
			HomeTeamName: txF.GetHome(),
			AwayTeamName: txF.GetAway(),
			MatchDate:    time.Now().Truncate(24 * time.Hour), // Set to today for testing
			Status:       txF.GetStatus(),
		}

		if f.HomeTeamName == "" { f.HomeTeamName = "TBD Home" }
		if f.AwayTeamName == "" { f.AwayTeamName = "TBD Away" }

		if err := w.fixtures.UpsertFixture(ctx, f); err != nil {
			log.Printf("[daily-sync] ERROR upserting fixture %s: %v", apiID, err)
		}
	}
	log.Println("[daily-sync] Fixture sync complete")
}

// PlaceholderUpsertFixture is a placeholder function to wrap database insertion logic
func (w *DailySyncWorker) PlaceholderUpsertFixture(ctx context.Context, f *queries.Fixture) error {
	if err := w.fixtures.UpsertFixture(ctx, f); err != nil {
		log.Printf("[daily-sync] ERROR upserting fixture %s: %v", f.ApiID, err)
		return err
	}
	log.Printf("[daily-sync] upserted fixture %s", f.ApiID)
	return nil
}
