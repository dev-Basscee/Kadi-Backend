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

// APIFootballFixture is a simplified representation of an API-Football fixture response.
// Real response has many more fields — expand as needed.
type APIFootballFixture struct {
	Fixture struct {
		ID     int    `json:"id"`
		Date   string `json:"date"`
		Status struct {
			Short   string `json:"short"` // "NS", "1H", "HT", "2H", "FT", etc.
			Elapsed *int   `json:"elapsed"`
		} `json:"status"`
	} `json:"fixture"`
	League struct {
		ID      int    `json:"id"`
		Name    string `json:"name"`
		Country string `json:"country"`
		Logo    string `json:"logo"`
	} `json:"league"`
	Teams struct {
		Home struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			Logo string `json:"logo"`
		} `json:"home"`
		Away struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			Logo string `json:"logo"`
		} `json:"away"`
	} `json:"teams"`
	Goals struct {
		Home *int `json:"home"`
		Away *int `json:"away"`
	} `json:"goals"`
	Odds []struct {
		Bookmakers []struct {
			Bets []struct {
				Name   string `json:"name"`
				Values []struct {
					Value string  `json:"value"`
					Odd   float64 `json:"odd,string"`
				} `json:"values"`
			} `json:"bets"`
		} `json:"bookmakers"`
	} `json:"odds"`
}

// DailySyncWorker fetches upcoming fixtures and base odds every 24 hours.
// It runs as a goroutine and blocks forever — cancel ctx to stop it.
type DailySyncWorker struct {
	cfg      *config.Config
	fixtures *queries.FixtureStore
	client   *http.Client
}

// NewDailySyncWorker constructs a DailySyncWorker.
func NewDailySyncWorker(cfg *config.Config, fixtures *queries.FixtureStore) *DailySyncWorker {
	return &DailySyncWorker{
		cfg:      cfg,
		fixtures: fixtures,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Run starts the daily sync loop. It performs an immediate sync on startup
// then waits for the configured interval (default 24h).
func (w *DailySyncWorker) Run(ctx context.Context) {
	interval := time.Duration(w.cfg.DailySyncIntervalHours) * time.Hour
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Printf("[daily-sync] worker started — interval: %s", interval)

	// Immediate first run
	w.sync(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Println("[daily-sync] worker stopped")
			return
		case <-ticker.C:
			w.sync(ctx)
		}
	}
}

// sync fetches today + tomorrow's fixtures and upserts them into the DB.
func (w *DailySyncWorker) sync(ctx context.Context) {
	log.Println("[daily-sync] syncing fixtures...")

	today := time.Now().Format("2006-01-02")
	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")

	for _, date := range []string{today, tomorrow} {
		fixtures, err := w.fetchFixturesForDate(ctx, date)
		if err != nil {
			log.Printf("[daily-sync] ERROR fetching fixtures for %s: %v", date, err)
			continue
		}

		upserted := 0
		for i := range fixtures {
			f := mapAPIFixture(&fixtures[i])
			if err := w.fixtures.UpsertFixture(ctx, f); err != nil {
				log.Printf("[daily-sync] ERROR upserting fixture %s: %v", f.ApiID, err)
				continue
			}
			upserted++
		}
		log.Printf("[daily-sync] %s — upserted %d/%d fixtures", date, upserted, len(fixtures))
	}
}

// fetchFixturesForDate calls the API-Football /fixtures endpoint for a given date.
func (w *DailySyncWorker) fetchFixturesForDate(ctx context.Context, date string) ([]APIFootballFixture, error) {
	if w.cfg.APIFootballKey == "" {
		// No API key configured — return empty (useful in dev with mock data)
		log.Println("[daily-sync] API_FOOTBALL_KEY not set, skipping external fetch")
		return nil, nil
	}

	url := fmt.Sprintf("%s/fixtures?date=%s&timezone=UTC", w.cfg.APIFootballURL, date)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-rapidapi-key", w.cfg.APIFootballKey)
	req.Header.Set("x-rapidapi-host", "v3.football.api-sports.io")

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("api-football request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	var result struct {
		Response []APIFootballFixture `json:"response"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	return result.Response, nil
}

// mapAPIFixture converts an API-Football response into our internal Fixture type.
func mapAPIFixture(api *APIFootballFixture) *queries.Fixture {
	matchDate, _ := time.Parse(time.RFC3339, api.Fixture.Date)

	status := mapStatus(api.Fixture.Status.Short)

	f := &queries.Fixture{
		ApiID:        fmt.Sprintf("apf-%d", api.Fixture.ID),
		Sport:        "football", // API-Football is football-only; extend for other APIs
		HomeTeamName: api.Teams.Home.Name,
		AwayTeamName: api.Teams.Away.Name,
		HomeTeamLogo: api.Teams.Home.Logo,
		AwayTeamLogo: api.Teams.Away.Logo,
		MatchDate:    matchDate,
		MatchTime:    matchDate.Format("15:04"),
		Status:       status,
		HomeScore:    api.Goals.Home,
		AwayScore:    api.Goals.Away,
		Minute:       api.Fixture.Status.Elapsed,
	}

	return f
}

// mapStatus converts API-Football status codes to our internal status enum.
func mapStatus(s string) string {
	switch s {
	case "NS", "TBD":
		return "upcoming"
	case "1H", "HT", "2H", "ET", "BT", "P", "INT", "LIVE":
		return "live"
	case "FT", "AET", "PEN":
		return "finished"
	case "PST", "CANC", "ABD", "AWD", "WO":
		return "postponed"
	default:
		return "upcoming"
	}
}
