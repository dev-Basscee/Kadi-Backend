package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds all environment-driven settings for the Kadi backend.
type Config struct {
	// Server
	Port string

	// Supabase / PostgreSQL
	DatabaseURL            string // Full Supabase Connection Pooler URL (pgbouncer mode)
	SupabaseJWTSecret      string // Used by the auth middleware to verify tokens
	SupabaseURL            string // Supabase Project URL
	SupabaseAnonKey        string // Supabase Anon Key for client connections
	SupabaseServiceRoleKey string // Supabase Service Role Key (Secret)
	RedisURL               string // Redis connection string

	// Third-party sports data APIs (TxLINE)
	TxLineStreamURL string // The SSE endpoint URL
	TxLineAPIKey    string // API Key for TxLINE

	// Google Gemini
	GeminiAPIKey string
	GeminiModel  string // e.g. "gemini-1.5-flash"

	// Environment
	Env string // "development" | "production"
}

// Load reads the .env file (if present) and populates a Config struct.
// Values already set in the OS environment take precedence over .env.
func Load() (*Config, error) {
	// Best-effort: load shared frontend .env.local, then local fallbacks
	_ = godotenv.Load("../Kadi/.env.local")
	_ = godotenv.Load(".env.local")
	_ = godotenv.Load(".env")

	cfg := &Config{
		Port:                   getEnv("PORT", "8080"),
		DatabaseURL:            mustEnv("DATABASE_URL"),
		SupabaseJWTSecret:      mustEnv("SUPABASE_JWT_SECRET"),
		SupabaseURL:            mustEnv("NEXT_PUBLIC_SUPABASE_URL"),
		SupabaseAnonKey:        mustEnv("NEXT_PUBLIC_SUPABASE_ANON_KEY"),
		SupabaseServiceRoleKey: mustEnv("SUPABASE_SERVICE_ROLE_KEY"),
		RedisURL:               getEnv("REDIS_URL", "redis://localhost:6379/0"),
		TxLineStreamURL:   mustEnv("TXLINE_STREAM_URL"),
		TxLineAPIKey:      mustEnv("TXLINE_API_KEY"),
		GeminiAPIKey:      mustEnv("GEMINI_API_KEY"),
		GeminiModel:       getEnv("GEMINI_MODEL", "gemini-1.5-flash"),
		Env:               getEnv("ENV", "development"),
	}

	// Validate DATABASE_URL is the pooler URL, not the direct IPv6 host.
	validateDatabaseURL(cfg.DatabaseURL)

	return cfg, nil
}

// IsDev returns true when running in development mode.
func (c *Config) IsDev() bool {
	return c.Env == "development"
}

// ─── helpers ────────────────────────────────────────────────────────────────

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required environment variable %q is not set", key))
	}
	return v
}

// validateDatabaseURL checks that DATABASE_URL points to the Supabase Connection
// Pooler and not the direct host. The direct host (db.<ref>.supabase.co)
// resolves to an IPv6 address which is unreachable on platforms like Render.
// The pooler URL (aws-0-<region>.pooler.supabase.com:6543) uses IPv4.
func validateDatabaseURL(url string) {
	// Skip validation for non-Supabase URLs (local Postgres, Docker, etc.)
	if !strings.Contains(url, "supabase") {
		return
	}
	if strings.Contains(url, "supabase.co") && !strings.Contains(url, "pooler.supabase.com") {
		panic(
			"[config] FATAL: DATABASE_URL is set to the direct Supabase host (db.<ref>.supabase.co).\n" +
				"Direct connections resolve to IPv6 and will fail on Render with 'network is unreachable'.\n" +
				"FIX: Go to Supabase > Project Settings > Database > Connection String.\n" +
				"Enable 'Use connection pooling' and copy the pooler URL.\n" +
				"Expected format: postgres://postgres.<ref>:<pw>@aws-0-<region>.pooler.supabase.com:6543/postgres",
		)
	}
}
