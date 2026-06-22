package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Client wraps a pgxpool.Pool and exposes named query methods.
// Using pgxpool ensures thread-safe, connection-pooled access to Supabase PostgreSQL.
type Client struct {
	Pool *pgxpool.Pool
}

// New creates and validates a new database connection pool.
// The dsn should point to the Supabase Connection Pooler (pgbouncer, port 6543)
// for optimal connection management in a high-concurrency Go service.
func New(ctx context.Context, dsn string) (*Client, error) {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("db: failed to parse DSN: %w", err)
	}

	// Pool tuning — adjust based on Supabase plan limits
	poolCfg.MaxConns = 20
	poolCfg.MinConns = 2
	poolCfg.MaxConnLifetime = 30 * time.Minute
	poolCfg.MaxConnIdleTime = 5 * time.Minute
	poolCfg.HealthCheckPeriod = 1 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("db: failed to create pool: %w", err)
	}

	// Validate connectivity immediately
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("db: ping failed — is DATABASE_URL correct? %w", err)
	}

	return &Client{Pool: pool}, nil
}

// Close drains and closes all pool connections gracefully.
func (c *Client) Close() {
	c.Pool.Close()
}
