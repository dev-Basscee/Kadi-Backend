package workers

import (
	"context"
	"log"

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

// Run starts the worker. Placeholder for new SSE stream payload connection.
func (w *DailySyncWorker) Run(ctx context.Context) {
	log.Printf("[daily-sync] worker started (waiting for SSE integration)")

	<-ctx.Done()
	log.Println("[daily-sync] worker stopped")
}

// PlaceholderUpsertFixture is a placeholder function to wrap database insertion logic
// so we can connect it to our new SSE stream payload.
func (w *DailySyncWorker) PlaceholderUpsertFixture(ctx context.Context, f *queries.Fixture) error {
	if err := w.fixtures.UpsertFixture(ctx, f); err != nil {
		log.Printf("[daily-sync] ERROR upserting fixture %s: %v", f.ApiID, err)
		return err
	}
	log.Printf("[daily-sync] upserted fixture %s", f.ApiID)
	return nil
}
