package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/kadi/backend/internal/config"
)

// TxLineHandler provides proxies to the TxLine REST endpoints (snapshot, historical, odds).
type TxLineHandler struct {
	cfg *config.Config
}

func NewTxLineHandler(cfg *config.Config) *TxLineHandler {
	return &TxLineHandler{cfg: cfg}
}

// fetchGuestJWT fetches a fresh guest JWT.
func (h *TxLineHandler) fetchGuestJWT(ctx context.Context) (string, error) {
	baseURL := h.cfg.TxLineStreamURL
	if idx := strings.Index(baseURL, "/api/"); idx != -1 {
		baseURL = baseURL[:idx]
	}
	authURL := baseURL + "/auth/guest/start"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("guest auth returned %d", resp.StatusCode)
	}

	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.Token, nil
}

// proxyTxLine proxy a GET request to TxLine API.
func (h *TxLineHandler) proxyTxLine(c *gin.Context, path string) {
	jwt, err := h.fetchGuestJWT(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to authenticate with TxLine"})
		return
	}

	baseURL := h.cfg.TxLineStreamURL
	if idx := strings.Index(baseURL, "/api/"); idx != -1 {
		baseURL = baseURL[:idx]
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, baseURL+path, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create request"})
		return
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("X-Api-Token", h.cfg.TxLineAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to fetch from TxLine"})
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	
	// Try to send it back as JSON
	c.Data(resp.StatusCode, "application/json", body)
}

// GetScoresSnapshot handles GET /api/v1/txline/scores/snapshot/:id
func (h *TxLineHandler) GetScoresSnapshot(c *gin.Context) {
	h.proxyTxLine(c, "/api/scores/snapshot/"+c.Param("id"))
}

// GetScoresUpdates handles GET /api/v1/txline/scores/updates/:id
func (h *TxLineHandler) GetScoresUpdates(c *gin.Context) {
	h.proxyTxLine(c, "/api/scores/updates/"+c.Param("id"))
}

// GetScoresHistorical handles GET /api/v1/txline/scores/historical/:id
func (h *TxLineHandler) GetScoresHistorical(c *gin.Context) {
	h.proxyTxLine(c, "/api/scores/historical/"+c.Param("id"))
}

// GetOddsSnapshot handles GET /api/v1/txline/odds/snapshot/:id
func (h *TxLineHandler) GetOddsSnapshot(c *gin.Context) {
	h.proxyTxLine(c, "/api/odds/snapshot/"+c.Param("id"))
}
