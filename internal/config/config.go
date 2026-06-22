package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

// Config holds all environment-driven settings for the Kadi backend.
type Config struct {
	// Server
	Port string

	// Supabase / PostgreSQL
	DatabaseURL   string // Full Supabase Connection Pooler URL (pgbouncer mode)
	SupabaseJWTSecret string // Used by the auth middleware to verify tokens
	RedisURL      string // Redis connection string

	// Third-party sports data APIs
	APIFootballKey string // api-football.com / Sportradar key
	APIFootballURL string

	// Google Gemini
	GeminiAPIKey string
	GeminiModel  string // e.g. "gemini-1.5-flash"

	// Workers
	LiveTickerIntervalSec  int // default: 12
	DailySyncIntervalHours int // default: 24

	// Environment
	Env string // "development" | "production"
}

// Load reads the .env file (if present) and populates a Config struct.
// Values already set in the OS environment take precedence over .env.
func Load() (*Config, error) {
	// Best-effort: load .env — ignore error if file doesn't exist in prod
	_ = godotenv.Load()

	cfg := &Config{
		Port:                   getEnv("PORT", "8080"),
		DatabaseURL:            mustEnv("DATABASE_URL"),
		SupabaseJWTSecret:      mustEnv("SUPABASE_JWT_SECRET"),
		RedisURL:               getEnv("REDIS_URL", "redis://localhost:6379/0"),
		APIFootballKey:         getEnv("API_FOOTBALL_KEY", ""),
		APIFootballURL:         getEnv("API_FOOTBALL_URL", "https://v3.football.api-sports.io"),
		GeminiAPIKey:           mustEnv("GEMINI_API_KEY"),
		GeminiModel:            getEnv("GEMINI_MODEL", "gemini-1.5-flash"),
		LiveTickerIntervalSec:  getEnvInt("LIVE_TICKER_INTERVAL_SEC", 12),
		DailySyncIntervalHours: getEnvInt("DAILY_SYNC_INTERVAL_HOURS", 24),
		Env:                    getEnv("ENV", "development"),
	}

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

func getEnvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	var n int
	_, err := fmt.Sscanf(v, "%d", &n)
	if err != nil {
		return fallback
	}
	return n
}
