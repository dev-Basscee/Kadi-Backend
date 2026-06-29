package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kadi/backend/internal/ai"
	"github.com/kadi/backend/internal/api"
	"github.com/kadi/backend/internal/config"
	"github.com/kadi/backend/internal/db"
	"github.com/kadi/backend/internal/db/queries"
	"github.com/kadi/backend/internal/workers"
)

func main() {
	// ─── Config ─────────────────────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// ─── Context with graceful shutdown ─────────────────────────────────────
	// Workers and the server all share this context.
	// Sending SIGINT (Ctrl-C) or SIGTERM (Docker stop) triggers cancellation.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// ─── Database ───────────────────────────────────────────────────────────
	log.Println("[main] connecting to database...")
	dbClient, err := db.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("[main] database connection failed: %v", err)
	}
	defer dbClient.Close()
	log.Println("[main] database connected ✓")

	// ─── AI Client ──────────────────────────────────────────────────────────
	log.Println("[main] initializing Gemini client...")
	geminiClient, err := ai.New(ctx, cfg.GeminiAPIKey, cfg.GeminiModel)
	if err != nil {
		log.Fatalf("[main] Gemini init failed: %v", err)
	}
	defer geminiClient.Close()
	log.Println("[main] Gemini client ready ✓")

	// ─── Redis ──────────────────────────────────────────────────────────────
	log.Println("[main] connecting to Redis...")
	redisClient, err := db.NewRedis(ctx, cfg.RedisURL)
	if err != nil {
		log.Fatalf("[main] Redis connection failed: %v", err)
	}
	defer redisClient.Close()
	log.Println("[main] Redis connected ✓")

	// ─── Stores ─────────────────────────────────────────────────────────────
	fixtureStore := queries.NewFixtureStore(dbClient.Pool)
	bankrollStore := queries.NewBankrollStore(dbClient.Pool)

	// ─── Background Workers ─────────────────────────────────────────────────
	// Each worker runs in its own goroutine and respects context cancellation.
	dailySync := workers.NewDailySyncWorker(cfg, fixtureStore)
	liveTicker := workers.NewLiveTickerWorker(cfg, fixtureStore, bankrollStore, redisClient)
	precompute := workers.NewPrecomputeWorker(fixtureStore, geminiClient, redisClient)

	go dailySync.Run(ctx)
	go liveTicker.Run(ctx)
	precompute.Start()
	log.Println("[main] background workers started ✓")

	// ─── HTTP Server ─────────────────────────────────────────────────────────
	router := api.NewRouter(cfg, dbClient, redisClient, geminiClient)

	server := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second, // allow time for Gemini streaming
		IdleTimeout:  60 * time.Second,
	}

	// Start server in a goroutine so it doesn't block shutdown logic
	go func() {
		log.Printf("[main] Kadi backend listening on :%s — env: %s", cfg.Port, cfg.Env)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[main] server error: %v", err)
		}
	}()

	// ─── Graceful Shutdown ───────────────────────────────────────────────────
	// Block until context is cancelled (SIGINT / SIGTERM)
	<-ctx.Done()
	log.Println("[main] shutdown signal received — draining...")

	// Give in-flight requests up to 10 seconds to complete
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("[main] HTTP server shutdown error: %v", err)
	}

	precompute.Stop()

	log.Println("[main] shutdown complete ✓")
}
