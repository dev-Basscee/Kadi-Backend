package db

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Client wraps a pgxpool.Pool and exposes named query methods.
// Using pgxpool ensures thread-safe, connection-pooled access to Supabase PostgreSQL.
type Client struct {
	Pool *pgxpool.Pool
}

// New creates and validates a new database connection pool.
// The dsn MUST point to the Supabase Connection Pooler (pgbouncer, port 6543)
// for optimal connection management and IPv4 compatibility (e.g. on Render).
// Direct Supabase hosts (db.<ref>.supabase.co) resolve to IPv6 and will fail
// on platforms that do not support outbound IPv6 connections.
func New(ctx context.Context, dsn string) (*Client, error) {
	// Warn early if the DSN looks like a direct connection instead of the pooler.
	// The pooler host pattern is: aws-0-<region>.pooler.supabase.com
	// The direct host pattern is: db.<project-ref>.supabase.co  (IPv6 — fails on Render)
	if strings.Contains(dsn, "supabase.co") && !strings.Contains(dsn, "pooler.supabase.com") {
		log.Println("[db] WARNING: DATABASE_URL appears to use the direct Supabase host.")
		log.Println("[db] WARNING: Direct connections resolve to IPv6 and will fail on Render.")
		log.Println("[db] WARNING: Use the Connection Pooler URL from Supabase Project Settings → Database.")
		log.Println("[db] WARNING: Expected format: postgres://postgres.<ref>:<pw>@aws-0-<region>.pooler.supabase.com:6543/postgres")
	}

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

	// Validate connectivity with retry + exponential backoff.
	// This guards against transient network blips on cold container starts.
	const maxAttempts = 5
	backoff := 1 * time.Second
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err = pool.Ping(ctx); err == nil {
			break
		}
		if attempt == maxAttempts {
			pool.Close()
			return nil, fmt.Errorf(
				"db: ping failed after %d attempts — is DATABASE_URL correct? "+
					"If using Supabase, ensure you are using the Connection Pooler URL (IPv4). Error: %w",
				maxAttempts, err,
			)
		}
		log.Printf("[db] ping attempt %d/%d failed: %v — retrying in %s", attempt, maxAttempts, err, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			pool.Close()
			return nil, fmt.Errorf("db: context cancelled while waiting for database: %w", ctx.Err())
		}
		backoff *= 2 // exponential backoff: 1s → 2s → 4s → 8s
	}

	return &Client{Pool: pool}, nil
}

// Close drains and closes all pool connections gracefully.
func (c *Client) Close() {
	c.Pool.Close()
}
