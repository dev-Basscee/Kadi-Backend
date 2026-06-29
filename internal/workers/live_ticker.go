package workers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/kadi/backend/internal/config"
	"github.com/kadi/backend/internal/db"
	"github.com/kadi/backend/internal/db/queries"
)

// LiveTickerWorker connects to the TxLINE SSE stream
// for live match updates (scores, minute, status changes).
// When a match finishes, it triggers the bet settlement pipeline.
type LiveTickerWorker struct {
	cfg      *config.Config
	fixtures *queries.FixtureStore
	bankroll *queries.BankrollStore
	rdb      *db.RedisClient

	// Track which fixture IDs were live on the last tick to detect transitions
	previouslyLive map[string]string // match_id -> previous status
}

// NewLiveTickerWorker constructs a LiveTickerWorker.
func NewLiveTickerWorker(
	cfg *config.Config,
	fixtures *queries.FixtureStore,
	bankroll *queries.BankrollStore,
	rdb *db.RedisClient,
) *LiveTickerWorker {
	return &LiveTickerWorker{
		cfg:      cfg,
		fixtures: fixtures,
		bankroll: bankroll,
		rdb:      rdb,
		previouslyLive: make(map[string]string),
	}
}

// Run starts the live ticker loop with exponential backoff.
func (w *LiveTickerWorker) Run(ctx context.Context) {
	log.Println("[live-ticker] worker started — connecting to TxLINE SSE")

	backoff := 1 * time.Second
	maxBackoff := 60 * time.Second

	for {
		select {
		case <-ctx.Done():
			log.Println("[live-ticker] worker stopped")
			return
		default:
			err := w.connectTxLineSSE(ctx)
			if err != nil {
				log.Printf("[live-ticker] TxLINE connection error: %v, retrying in %v...", err, backoff)
				select {
				case <-time.After(backoff):
				case <-ctx.Done():
					return
				}
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			} else {
				// Reset backoff on successful connection that ended naturally
				backoff = 1 * time.Second
			}
		}
	}
}

type TxLineUpdate struct {
	MatchID   string `json:"match_id"`
	Status    string `json:"status"`
	HomeScore int    `json:"home_score"`
	AwayScore int    `json:"away_score"`
	Minute    int    `json:"minute"`
	Odds      struct {
		Home float64 `json:"home"`
		Draw float64 `json:"draw"`
		Away float64 `json:"away"`
	} `json:"odds"`
	TxLineSignature string          `json:"txline_signature"`
	MerkleRoot      string          `json:"merkle_root"`
	ProofReceipt    json.RawMessage `json:"proof_receipt"`
}

func (w *LiveTickerWorker) connectTxLineSSE(ctx context.Context) error {
	url := w.cfg.TxLineStreamURL

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	// Set the API Key
	if w.cfg.TxLineAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+w.cfg.TxLineAPIKey)
	}

	client := &http.Client{Timeout: 0} // No timeout for SSE connection
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sse connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		
		dataStr := strings.TrimPrefix(line, "data: ")
		if dataStr == "" {
			continue
		}

		var update TxLineUpdate
		if err := json.Unmarshal([]byte(dataStr), &update); err != nil {
			log.Printf("[live-ticker] failed to parse update: %v", err)
			continue
		}

		if err := w.fixtures.UpdateLiveScoreWithTxLine(
			ctx, update.MatchID, update.Status, update.HomeScore, update.AwayScore, update.Minute,
			update.TxLineSignature, update.MerkleRoot, update.ProofReceipt,
		); err != nil {
			log.Printf("[live-ticker] ERROR updating %s: %v", update.MatchID, err)
			continue
		}

		// Broadcast to Redis Pub/Sub for frontend clients bypassing Supabase Realtime
		if err := w.rdb.Client.Publish(ctx, "txline:updates", dataStr).Err(); err != nil {
			log.Printf("[live-ticker] ERROR publishing to Redis: %v", err)
		}

		w.previouslyLive[update.MatchID] = update.Status

		if update.Status == "finished" {
			log.Printf("[live-ticker] match FINISHED: %s — triggering settlement", update.MatchID)
			go w.settleMatchBets(ctx, update.MatchID, update.HomeScore, update.AwayScore)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("sse read error: %w", err)
	}
	return nil
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
