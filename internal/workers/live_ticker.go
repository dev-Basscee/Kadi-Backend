package workers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kadi/backend/internal/config"
	"github.com/kadi/backend/internal/db"
	"github.com/kadi/backend/internal/db/queries"
)

// ─── TxLINE Auth ─────────────────────────────────────────────────────────────

// txLineGuestAuthResponse is the response from POST /auth/guest/start.
type txLineGuestAuthResponse struct {
	Token string `json:"token"`
}

// fetchGuestJWT calls /auth/guest/start on the TxLINE base URL and returns
// a short-lived session JWT. This must be sent as Authorization: Bearer <jwt>
// alongside X-Api-Token: <long-lived-api-token> on every SSE request.
func fetchGuestJWT(ctx context.Context, baseURL string) (string, error) {
	authURL := strings.TrimRight(baseURL, "/scores/stream") + "/auth/guest/start"

	// Derive the base URL properly: everything before /api/
	// e.g. https://txline-dev.txodds.com/api/scores/stream -> https://txline-dev.txodds.com
	if idx := strings.Index(baseURL, "/api/"); idx != -1 {
		authURL = baseURL[:idx] + "/auth/guest/start"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", fmt.Errorf("txline: build auth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("txline: guest auth request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("txline: guest auth returned %d: %s", resp.StatusCode, string(body))
	}

	var result txLineGuestAuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("txline: failed to decode guest auth response: %w", err)
	}
	if result.Token == "" {
		return "", fmt.Errorf("txline: guest auth returned empty token")
	}
	return result.Token, nil
}

// ─── TxLINE Event types ───────────────────────────────────────────────────────

// TxLineScore holds per-half and total score data for one participant.
type TxLineScore struct {
	H1    map[string]int `json:"H1"`
	HT    map[string]int `json:"HT"`
	H2    map[string]int `json:"H2"`
	Total map[string]int `json:"Total"`
}

// TxLineClock holds the running clock state.
type TxLineClock struct {
	Running bool `json:"Running"`
	Seconds int  `json:"Seconds"`
}

// TxLineEvent is the real SSE event shape returned by TxLINE (confirmed via
// live test on 2026-06-29). Fields are a superset; unknown fields are ignored.
type TxLineEvent struct {
	FixtureID   int    `json:"FixtureId"`
	GameState   string `json:"GameState"`
	StartTime   int64  `json:"StartTime"`
	SportID     int    `json:"SportId"`
	CompetitionID int  `json:"CompetitionId"`
	CountryID   int    `json:"CountryId"`
	Action      string `json:"Action"` // "goal", "substitution", "standby", "possible", etc.
	StatusID    int    `json:"StatusId"`
	Confirmed   bool   `json:"Confirmed"`
	Seq         int    `json:"Seq"`

	Clock TxLineClock `json:"Clock"`

	Score struct {
		Participant1 TxLineScore `json:"Participant1"`
		Participant2 TxLineScore `json:"Participant2"`
	} `json:"Score"`

	// Legacy / mapped fields (kept for DB compatibility)
	MatchID         string          `json:"match_id"`
	Status          string          `json:"status"`
	HomeScore       int             `json:"home_score"`
	AwayScore       int             `json:"away_score"`
	Minute          int             `json:"minute"`
	TxLineSignature string          `json:"txline_signature"`
	MerkleRoot      string          `json:"merkle_root"`
	ProofReceipt    json.RawMessage `json:"proof_receipt"`
}

// toMatchID derives a string match ID from the numeric FixtureId.
func (e *TxLineEvent) toMatchID() string {
	if e.MatchID != "" {
		return e.MatchID
	}
	return fmt.Sprintf("%d", e.FixtureID)
}

// toHomeScore extracts the total home goals from Score, falling back to HomeScore.
func (e *TxLineEvent) toHomeScore() int {
	if goals, ok := e.Score.Participant1.Total["Goals"]; ok {
		return goals
	}
	return e.HomeScore
}

// toAwayScore extracts the total away goals from Score, falling back to AwayScore.
func (e *TxLineEvent) toAwayScore() int {
	if goals, ok := e.Score.Participant2.Total["Goals"]; ok {
		return goals
	}
	return e.AwayScore
}

// toMinute converts clock seconds to match minute.
func (e *TxLineEvent) toMinute() int {
	if e.Minute != 0 {
		return e.Minute
	}
	return e.Clock.Seconds / 60
}

// toStatus maps TxLINE StatusId / GameState to the app's status strings.
func (e *TxLineEvent) toStatus() string {
	if e.Status != "" {
		return e.Status
	}
	switch e.StatusID {
	case 1:
		return "scheduled"
	case 2:
		return "live"
	case 3:
		return "finished"
	case 4:
		return "live" // in-progress per confirmed test data
	default:
		if e.GameState != "" {
			return e.GameState
		}
		return "unknown"
	}
}

// ─── LiveTickerWorker ─────────────────────────────────────────────────────────

// LiveTickerWorker connects to the TxLINE SSE stream
// for live match updates (scores, minute, status changes).
// When a match finishes, it triggers the bet settlement pipeline.
//
// Auth: TxLINE requires TWO credentials per request (confirmed from OpenAPI spec):
//
//	Authorization: Bearer <short-lived guest JWT>   — refreshed on each (re)connect
//	X-Api-Token:   <long-lived API token>            — from TXLINE_API_KEY env var
type LiveTickerWorker struct {
	cfg      *config.Config
	fixtures *queries.FixtureStore
	bankroll *queries.BankrollStore
	rdb      *db.RedisClient

	// jwt is the cached guest JWT; refreshed on each (re)connect.
	jwtMu sync.Mutex
	jwt   string

	// Track which fixture IDs were live on the last tick to detect transitions.
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
		cfg:            cfg,
		fixtures:       fixtures,
		bankroll:       bankroll,
		rdb:            rdb,
		previouslyLive: make(map[string]string),
	}
}

// Run starts the live ticker loops with exponential backoff.
func (w *LiveTickerWorker) Run(ctx context.Context) {
	log.Println("[live-ticker] worker started — connecting to TxLINE SSE")

	go w.runStream(ctx, "scores", w.connectTxLineScoresSSE)
	go w.runStream(ctx, "odds", w.connectTxLineOddsSSE)

	<-ctx.Done()
	log.Println("[live-ticker] worker stopped")
}

func (w *LiveTickerWorker) runStream(ctx context.Context, name string, connectFunc func(context.Context) error) {
	backoff := 1 * time.Second
	maxBackoff := 60 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
			err := connectFunc(ctx)
			if err != nil {
				log.Printf("[live-ticker][%s] TxLINE connection error: %v, retrying in %v...", name, err, backoff)
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
				backoff = 1 * time.Second
			}
		}
	}
}

// connectTxLineScoresSSE fetches a fresh guest JWT, then opens the scores SSE stream.
func (w *LiveTickerWorker) connectTxLineScoresSSE(ctx context.Context) error {
	// ── Step 1: fetch a fresh short-lived guest JWT ───────────────────────────
	jwt, err := fetchGuestJWT(ctx, w.cfg.TxLineStreamURL)
	if err != nil {
		return fmt.Errorf("failed to fetch guest JWT: %w", err)
	}
	w.jwtMu.Lock()
	w.jwt = jwt
	w.jwtMu.Unlock()
	log.Printf("[live-ticker] guest JWT acquired (%.20s...)", jwt)

	// ── Step 2: open SSE stream with BOTH required headers ────────────────────
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.cfg.TxLineStreamURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+jwt)         // short-lived session JWT
	req.Header.Set("X-Api-Token", w.cfg.TxLineAPIKey)      // long-lived API token

	client := &http.Client{Timeout: 0} // no timeout — SSE is long-lived
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sse connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	log.Println("[live-ticker] SSE stream connected — receiving events")

	// ── Step 3: process incoming SSE events ───────────────────────────────────
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

		var event TxLineEvent
		if err := json.Unmarshal([]byte(dataStr), &event); err != nil {
			log.Printf("[live-ticker] failed to parse event: %v", err)
			continue
		}

		matchID  := event.toMatchID()
		status   := event.toStatus()
		home     := event.toHomeScore()
		away     := event.toAwayScore()
		minute   := event.toMinute()

		if err := w.fixtures.UpdateLiveScoreWithTxLine(
			ctx, matchID, status, home, away, minute,
			event.TxLineSignature, event.MerkleRoot, event.ProofReceipt,
		); err != nil {
			log.Printf("[live-ticker] ERROR updating fixture %s: %v", matchID, err)
			continue
		}

		// Broadcast raw event to Redis Pub/Sub for frontend clients
		if err := w.rdb.Client.Publish(ctx, "txline:updates", dataStr).Err(); err != nil {
			log.Printf("[live-ticker] ERROR publishing to Redis: %v", err)
		}

		w.previouslyLive[matchID] = status

		if status == "finished" {
			log.Printf("[live-ticker] match FINISHED: %s — triggering settlement", matchID)
			go w.settleMatchBets(ctx, matchID, home, away)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("sse read error: %w", err)
	}
	return nil
}

// connectTxLineOddsSSE fetches a fresh guest JWT, then opens the odds SSE stream.
func (w *LiveTickerWorker) connectTxLineOddsSSE(ctx context.Context) error {
	baseURL := w.cfg.TxLineStreamURL
	if idx := strings.Index(baseURL, "/api/"); idx != -1 {
		baseURL = baseURL[:idx]
	}

	jwt, err := fetchGuestJWT(ctx, w.cfg.TxLineStreamURL)
	if err != nil {
		return fmt.Errorf("failed to fetch guest JWT for odds: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/odds/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("X-Api-Token", w.cfg.TxLineAPIKey)

	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("odds sse connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("odds unexpected status %d: %s", resp.StatusCode, string(body))
	}

	log.Println("[live-ticker] Odds SSE stream connected — receiving events")

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

		// Try to parse basic odds info if available, or just broadcast
		var event struct {
			FixtureID interface{} `json:"FixtureId"`
			FixtureID2 interface{} `json:"fixtureId"`
			Prices    []struct {
				Selection string  `json:"selection"`
				Price     float64 `json:"price"`
			} `json:"prices"`
		}

		// Broadcast raw event to Redis Pub/Sub for frontend clients
		if err := w.rdb.Client.Publish(ctx, "txline:odds", dataStr).Err(); err != nil {
			log.Printf("[live-ticker] ERROR publishing odds to Redis: %v", err)
		}

		if err := json.Unmarshal([]byte(dataStr), &event); err == nil {
			var matchID string
			if event.FixtureID != nil {
				matchID = fmt.Sprintf("%v", event.FixtureID)
			} else if event.FixtureID2 != nil {
				matchID = fmt.Sprintf("%v", event.FixtureID2)
			}

			if matchID != "" && len(event.Prices) > 0 {
				var home, draw, away float64
				for _, p := range event.Prices {
					switch p.Selection {
					case "1", "home": home = p.Price
					case "X", "draw": draw = p.Price
					case "2", "away": away = p.Price
					}
				}
				if home > 0 && draw > 0 && away > 0 {
					w.fixtures.UpdateOdds(ctx, matchID, home, draw, away)
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("odds sse read error: %w", err)
	}
	return nil
}

// ─── Settlement ───────────────────────────────────────────────────────────────

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
