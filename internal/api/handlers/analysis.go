package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kadi/backend/internal/ai"
	"github.com/kadi/backend/internal/db"
	"github.com/kadi/backend/internal/db/queries"
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
func (h *AnalysisHandler) DeepDive(c *gin.Context) {
	req, err := h.parseDeepDiveReq(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	fixture, err := h.fixtures.GetByID(c.Request.Context(), req.MatchID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "match not found"})
		return
	}

	// Skip cache if we are asking for real-time TxLINE analysis
	if req.TxLineData == nil && h.serveCachedAnalysis(c, fixture) {
		return
	}

	h.generateAndServeAnalysis(c, fixture, req.TxLineData)
}

type DeepDiveRequest struct {
	MatchID    string                `json:"match_id" binding:"required"`
	TxLineData *ai.TxLineDataPayload `json:"txline_data,omitempty"`
}

func (h *AnalysisHandler) parseDeepDiveReq(c *gin.Context) (*DeepDiveRequest, error) {
	var req DeepDiveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return nil, err
	}
	return &req, nil
}

func (h *AnalysisHandler) serveCachedAnalysis(c *gin.Context, fixture *queries.Fixture) bool {
	ctx := c.Request.Context()
	cacheKey := fmt.Sprintf("analysis:fixture:%s", fixture.ID)
	cachedJSON, err := h.rdb.Client.Get(ctx, cacheKey).Result()
	
	if err != nil || cachedJSON == "" {
		return false
	}

	var cachedAnalysis ai.MatchAnalysis
	if json.Unmarshal([]byte(cachedJSON), &cachedAnalysis) == nil {
		h.respondWithAnalysis(c, fixture, cachedAnalysis, true)
		return true
	}
	return false
}

func (h *AnalysisHandler) generateAndServeAnalysis(c *gin.Context, fixture *queries.Fixture, txData *ai.TxLineDataPayload) {
	ctx := c.Request.Context()
	modelName := "gemini-1.5-flash"
	if c.GetBool("isPremium") {
		modelName = "gemini-3.1-pro-preview"
	}

	analysis, err := h.gemini.AnalyzeMatch(ctx, fixture, txData, modelName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI analysis failed: " + err.Error()})
		return
	}

	// Cache unless it's a real-time customized analysis
	if txData == nil {
		h.cacheAnalysis(ctx, fixture.ID, analysis)
	}
	h.respondWithAnalysis(c, fixture, analysis, false)
}

func (h *AnalysisHandler) cacheAnalysis(ctx context.Context, fixtureID string, analysis *ai.MatchAnalysis) {
	cacheKey := fmt.Sprintf("analysis:fixture:%s", fixtureID)
	if analysisBytes, err := json.Marshal(analysis); err == nil {
		h.rdb.Client.Set(ctx, cacheKey, analysisBytes, 12*time.Hour)
	}
}

func (h *AnalysisHandler) respondWithAnalysis(c *gin.Context, fixture *queries.Fixture, analysis interface{}, cached bool) {
	c.JSON(http.StatusOK, gin.H{
		"match_id": fixture.ID,
		"fixture": gin.H{
			"home": fixture.HomeTeamName,
			"away": fixture.AwayTeamName,
			"date": fixture.MatchDate,
		},
		"analysis": analysis,
		"cached":   cached,
	})
}

// Explain returns a concise confidence explanation for why a prediction was made.
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
