package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kadi/backend/internal/ai"
	"github.com/kadi/backend/internal/db"
	"github.com/kadi/backend/internal/db/queries"
	"github.com/redis/go-redis/v9"
)

// AnalysisHandler powers the DeepDiveModal and AIAnalysisExplainer.
type AnalysisHandler struct {
	fixtures *queries.FixtureStore
	gemini   *ai.GeminiClient
	rdb      *db.RedisClient
}

// NewAnalysisHandler constructs an AnalysisHandler.
func NewAnalysisHandler(fixtures *queries.FixtureStore, gemini *ai.GeminiClient, rdb *db.RedisClient) *AnalysisHandler {
	return &AnalysisHandler{fixtures: fixtures, gemini: gemini, rdb: rdb}
}

// DeepDive generates a full AI analysis for a specific match.
// This is the most compute-intensive endpoint — it fetches context from the DB
// and streams a structured response from Gemini.
//
// POST /api/v1/analysis/deep-dive
// Body: { "match_id": "<uuid>" }
func (h *AnalysisHandler) DeepDive(c *gin.Context) {
	var req struct {
		MatchID string `json:"match_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 1. Fetch fixture context from database
	fixture, err := h.fixtures.GetByID(c.Request.Context(), req.MatchID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "match not found"})
		return
	}

	// 2. Check Redis Semantic Cache
	ctx := c.Request.Context()
	cacheKey := fmt.Sprintf("analysis:fixture:%s", fixture.ID)
	cachedJSON, err := h.rdb.Client.Get(ctx, cacheKey).Result()
	
	if err == nil && cachedJSON != "" {
		// Cache Hit
		var cachedAnalysis ai.MatchAnalysis
		if json.Unmarshal([]byte(cachedJSON), &cachedAnalysis) == nil {
			c.JSON(http.StatusOK, gin.H{
				"match_id": req.MatchID,
				"fixture": gin.H{
					"home": fixture.HomeTeamName,
					"away": fixture.AwayTeamName,
					"date": fixture.MatchDate,
				},
				"analysis": cachedAnalysis,
				"cached":   true,
			})
			return
		}
	}

	// 3. Determine Model Tier
	isPremium := c.GetBool("isPremium") // We'll assume auth middleware sets this
	modelName := "gemini-1.5-flash"
	if isPremium {
		modelName = "gemini-3.1-pro-preview"
	}

	// 4. Call Gemini for deep analysis (Cache Miss)
	analysis, err := h.gemini.AnalyzeMatch(ctx, fixture, modelName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI analysis failed: " + err.Error()})
		return
	}

	// 5. Save to Redis Cache (12 hour TTL)
	if analysisBytes, err := json.Marshal(analysis); err == nil {
		h.rdb.Client.Set(ctx, cacheKey, analysisBytes, 12*time.Hour)
	}

	c.JSON(http.StatusOK, gin.H{
		"match_id": req.MatchID,
		"fixture": gin.H{
			"home": fixture.HomeTeamName,
			"away": fixture.AwayTeamName,
			"date": fixture.MatchDate,
		},
		"analysis": analysis,
		"cached":   false,
	})
}

// Explain returns a concise confidence explanation for why a prediction was made.
//
// GET /api/v1/analysis/explain/:match_id
func (h *AnalysisHandler) Explain(c *gin.Context) {
	matchID := c.Param("match_id")

	fixture, err := h.fixtures.GetByID(c.Request.Context(), matchID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "match not found"})
		return
	}

	explanation, err := h.gemini.ExplainPrediction(c.Request.Context(), fixture)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"match_id":    matchID,
		"explanation": explanation,
	})
}
