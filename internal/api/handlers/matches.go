package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kadi/backend/internal/db/queries"
)

// MatchHandler handles fixture-related HTTP endpoints.
type MatchHandler struct {
	fixtures *queries.FixtureStore
}

// NewMatchHandler constructs a MatchHandler.
func NewMatchHandler(fixtures *queries.FixtureStore) *MatchHandler {
	return &MatchHandler{fixtures: fixtures}
}

// GetTodaysMatches returns all fixtures for today ordered by kick-off time.
//
// GET /api/v1/matches
// Query: ?sport=football (optional filter)
func (h *MatchHandler) GetTodaysMatches(c *gin.Context) {
	today := time.Now()
	fixtures, err := h.fixtures.ListByDate(c.Request.Context(), today)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	sport := c.Query("sport")
	if sport != "" && sport != "all" {
		filtered := fixtures[:0]
		for _, f := range fixtures {
			if f.Sport == sport {
				filtered = append(filtered, f)
			}
		}
		fixtures = filtered
	}

	c.JSON(http.StatusOK, gin.H{
		"data":  fixtures,
		"count": len(fixtures),
		"date":  today.Format("2006-01-02"),
	})
}

// GetLiveMatches returns all currently live fixtures.
//
// GET /api/v1/matches/live
func (h *MatchHandler) GetLiveMatches(c *gin.Context) {
	fixtures, err := h.fixtures.ListLive(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": fixtures, "count": len(fixtures)})
}

// GetMatchByID returns full details for a single fixture.
//
// GET /api/v1/matches/:id
func (h *MatchHandler) GetMatchByID(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "match id is required"})
		return
	}

	fixture, err := h.fixtures.GetByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "match not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": fixture})
}

// GetMatchVerificationData returns TxLINE cryptographic signatures and Merkle proofs for a match.
//
// GET /api/v1/matches/:id/verify
func (h *MatchHandler) GetMatchVerificationData(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "match id is required"})
		return
	}

	fixture, err := h.fixtures.GetByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "match not found"})
		return
	}

	if fixture.TxlineSignature == nil || fixture.MerkleRoot == nil || len(fixture.ProofReceipt) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "verification data not available for this match"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"match_id":         fixture.ID,
		"txline_signature": *fixture.TxlineSignature,
		"merkle_root":      *fixture.MerkleRoot,
		"proof_receipt":    json.RawMessage(fixture.ProofReceipt),
	})
}
