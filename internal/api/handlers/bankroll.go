package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/kadi/backend/internal/api/middleware"
	"github.com/kadi/backend/internal/db/queries"
)

// BankrollHandler handles all bankroll and bet slip endpoints.
type BankrollHandler struct {
	bankroll *queries.BankrollStore
}

// NewBankrollHandler constructs a BankrollHandler.
func NewBankrollHandler(bankroll *queries.BankrollStore) *BankrollHandler {
	return &BankrollHandler{bankroll: bankroll}
}

// GetBalance returns the user's current bankroll balance.
//
// GET /api/v1/bankroll/balance
// Requires: Authorization: Bearer <supabase-jwt>
func (h *BankrollHandler) GetBalance(c *gin.Context) {
	userID := middleware.GetUserID(c)

	balance, err := h.bankroll.GetBalance(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"balance": balance, "user_id": userID})
}

// GetLedger returns the user's transaction history.
//
// GET /api/v1/bankroll/ledger?limit=50
// Requires: Authorization: Bearer <supabase-jwt>
func (h *BankrollHandler) GetLedger(c *gin.Context) {
	userID := middleware.GetUserID(c)

	limit := 50
	if l := c.Query("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	entries, err := h.bankroll.GetLedger(c.Request.Context(), userID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": entries, "count": len(entries)})
}

// PlaceBet creates a new bet slip and deducts the stake atomically.
//
// POST /api/v1/bankroll/bets
// Requires: Authorization: Bearer <supabase-jwt>
// Body:
//
//	{
//	  "stake_amount": 50.00,
//	  "legs": [
//	    { "fixture_id": "<uuid>", "selection": "home_win", "odds": 1.85 }
//	  ]
//	}
func (h *BankrollHandler) PlaceBet(c *gin.Context) {
	userID := middleware.GetUserID(c)

	var req struct {
		StakeAmount float64 `json:"stake_amount" binding:"required,gt=0"`
		Legs        []struct {
			FixtureID string  `json:"fixture_id" binding:"required"`
			Selection string  `json:"selection" binding:"required"`
			Odds      float64 `json:"odds" binding:"required,gt=1"`
		} `json:"legs" binding:"required,min=1"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Calculate accumulator total odds and potential return
	totalOdds := 1.0
	for _, leg := range req.Legs {
		totalOdds *= leg.Odds
	}
	potentialReturn := req.StakeAmount * totalOdds

	// Build the slip struct
	slip := &queries.BetSlip{
		UserID:          userID,
		StakeAmount:     req.StakeAmount,
		TotalOdds:       totalOdds,
		PotentialReturn: potentialReturn,
	}
	for _, l := range req.Legs {
		slip.Legs = append(slip.Legs, queries.BetSlipLeg{
			FixtureID: l.FixtureID,
			Selection: l.Selection,
			Odds:      l.Odds,
		})
	}

	if err := h.bankroll.PlaceBet(c.Request.Context(), userID, slip); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"slip_id":          slip.ID,
		"total_odds":       totalOdds,
		"potential_return": potentialReturn,
		"message":          "Bet placed successfully",
	})
}
