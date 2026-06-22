package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/kadi/backend/internal/ai"
	"github.com/kadi/backend/internal/db"
	"github.com/kadi/backend/internal/db/queries"
	"github.com/robfig/cron/v3"
)

// PrecomputeWorker runs a cron job to pre-generate AI analyses for popular upcoming matches.
// This ensures cache hits during peak hours, significantly improving latency and reducing costs.
type PrecomputeWorker struct {
	fixtures *queries.FixtureStore
	gemini   *ai.GeminiClient
	rdb      *db.RedisClient
	cron     *cron.Cron
}

// NewPrecomputeWorker constructs the worker.
func NewPrecomputeWorker(fixtures *queries.FixtureStore, gemini *ai.GeminiClient, rdb *db.RedisClient) *PrecomputeWorker {
	c := cron.New(cron.WithLocation(time.UTC))
	return &PrecomputeWorker{
		fixtures: fixtures,
		gemini:   gemini,
		rdb:      rdb,
		cron:     c,
	}
}

// Start begins the cron scheduler. Call Stop() when shutting down.
func (w *PrecomputeWorker) Start() {
	// Run every day at 3:00 AM UTC
	_, err := w.cron.AddFunc("0 3 * * *", func() {
		w.runPrecomputeBatch(context.Background())
	})
	if err != nil {
		log.Printf("[precompute] failed to schedule cron: %v", err)
		return
	}

	w.cron.Start()
	log.Println("[precompute] worker scheduled for 3:00 AM UTC daily")
}

// Stop gracefully shuts down the cron scheduler.
func (w *PrecomputeWorker) Stop() {
	w.cron.Stop()
}

func (w *PrecomputeWorker) runPrecomputeBatch(ctx context.Context) {
	log.Println("[precompute] starting batch job...")

	today := time.Now().UTC()
	tomorrow := today.AddDate(0, 0, 1)

	// Fetch fixtures for tomorrow
	fixtures, err := w.fixtures.ListByDate(ctx, tomorrow)
	if err != nil {
		log.Printf("[precompute] failed to fetch fixtures: %v", err)
		return
	}

	// Filter or sort to get the top 20 most popular fixtures
	// For this example, we just take the first 20 fixtures available
	limit := 20
	if len(fixtures) < limit {
		limit = len(fixtures)
	}
	topFixtures := fixtures[:limit]

	processed := 0
	for _, fixture := range topFixtures {
		cacheKey := fmt.Sprintf("analysis:fixture:%s", fixture.ID)

		// Check if it's already cached
		if err := w.rdb.Client.Get(ctx, cacheKey).Err(); err == nil {
			continue // Already in cache
		}

		// Use the cost-effective model for batch processing
		analysis, err := w.gemini.AnalyzeMatch(ctx, &fixture, "gemini-1.5-flash")
		if err != nil {
			log.Printf("[precompute] failed analysis for %s: %v", fixture.ID, err)
			continue
		}

		// Save to cache with 24-hour TTL
		if analysisBytes, err := json.Marshal(analysis); err == nil {
			w.rdb.Client.Set(ctx, cacheKey, analysisBytes, 24*time.Hour)
			processed++
		}

		// Brief delay to respect rate limits
		time.Sleep(2 * time.Second)
	}

	log.Printf("[precompute] batch job complete. Pre-cached %d fixtures.", processed)
}
