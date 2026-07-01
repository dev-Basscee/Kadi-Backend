package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kadi/backend/internal/ai"
	authMiddleware "github.com/kadi/backend/internal/api/middleware"
	"github.com/kadi/backend/internal/api/handlers"
	"github.com/kadi/backend/internal/config"
	"github.com/kadi/backend/internal/db"
	"github.com/kadi/backend/internal/db/queries"
)

// NewRouter wires up all routes and returns a configured *gin.Engine.
// All route groups, middleware, and handler dependencies are assembled here.
func NewRouter(cfg *config.Config, dbClient *db.Client, redisClient *db.RedisClient, gemini *ai.GeminiClient) *gin.Engine {
	if cfg.IsDev() {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()

	// ─── Global Middleware ───────────────────────────────────────────────────
	r.Use(gin.Recovery()) // recover from panics and return 500
	r.Use(corsMiddleware())
	r.Use(requestLogger())

	// ─── Stores ─────────────────────────────────────────────────────────────
	fixtureStore := queries.NewFixtureStore(dbClient.Pool)
	bankrollStore := queries.NewBankrollStore(dbClient.Pool)

	// ─── Handlers ───────────────────────────────────────────────────────────
	matchHandler := handlers.NewMatchHandler(fixtureStore)
	bankrollHandler := handlers.NewBankrollHandler(bankrollStore)
	analysisHandler := handlers.NewAnalysisHandler(fixtureStore, gemini, redisClient)
	txlineHandler := handlers.NewTxLineHandler(cfg)

	// ─── Health Check (public) ──────────────────────────────────────────────
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":    "ok",
			"service":   "kadi-backend",
			"timestamp": time.Now().UTC(),
		})
	})

	// ─── API v1 ─────────────────────────────────────────────────────────────
	v1 := r.Group("/api/v1")

	// Public routes (no auth required)
	public := v1.Group("")
	{
		// Matches
		public.GET("/matches", matchHandler.GetTodaysMatches)
		public.GET("/matches/live", matchHandler.GetLiveMatches)
		public.GET("/matches/:id", matchHandler.GetMatchByID)
		public.GET("/matches/:id/verify", matchHandler.GetMatchVerificationData)

		// TxLine Proxies
		public.GET("/txline/scores/snapshot/:id", txlineHandler.GetScoresSnapshot)
		public.GET("/txline/scores/updates/:id", txlineHandler.GetScoresUpdates)
		public.GET("/txline/scores/historical/:id", txlineHandler.GetScoresHistorical)
		public.GET("/txline/odds/snapshot/:id", txlineHandler.GetOddsSnapshot)

		// Public analysis (explanation snippet)
		public.GET("/analysis/explain/:match_id", analysisHandler.Explain)
	}

	// Protected routes (Supabase JWT required)
	protected := v1.Group("")
	protected.Use(authMiddleware.Middleware(cfg.SupabaseJWTSecret))
	{
		// Deep dive (premium AI analysis) - protected by rate limiting (3/day)
		protected.POST("/analysis/deep-dive", authMiddleware.RateLimit(redisClient, 3), analysisHandler.DeepDive)

		// Bankroll
		protected.GET("/bankroll/balance", bankrollHandler.GetBalance)
		protected.GET("/bankroll/ledger", bankrollHandler.GetLedger)
		protected.POST("/bankroll/bets", bankrollHandler.PlaceBet)
	}

	return r
}

// ─── Middleware helpers ──────────────────────────────────────────────────────

// corsMiddleware sets permissive CORS headers for the Next.js frontend.
// Tighten AllowOrigins in production to your Vercel domain.
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Accept, Authorization")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// requestLogger logs method, path, latency, and status for every request.
func requestLogger() gin.HandlerFunc {
	return gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		return "[kadi] " + param.TimeStamp.Format(time.RFC3339) +
			" | " + param.Method +
			" " + param.Path +
			" | " + param.Latency.String() +
			" | " + http.StatusText(param.StatusCode) + "\n"
	})
}
