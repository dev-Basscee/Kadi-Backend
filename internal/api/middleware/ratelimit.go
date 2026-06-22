package middleware

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/kadi/backend/internal/db"
)

// RateLimit enforces a daily quota on a specific endpoint (e.g., Deep Dive).
// Free users are limited to `limit` requests per day.
// Note: This middleware assumes auth.Middleware has already run and injected the user ID.
func RateLimit(rdb *db.RedisClient, limit int) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetString("userID")
		if userID == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
			return
		}

		// (Optional) Check if user is premium to bypass rate limit
		isPremium := c.GetBool("isPremium")
		if isPremium {
			c.Next()
			return
		}

		ctx := context.Background()
		today := time.Now().UTC().Format("2006-01-02")
		key := fmt.Sprintf("ratelimit:%s:deepdive:%s", userID, today)

		// Increment the counter
		count, err := rdb.Client.Incr(ctx, key).Result()
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "rate limit check failed"})
			return
		}

		// If this is the first request of the day, set expiration to 24h
		if count == 1 {
			rdb.Client.Expire(ctx, key, 24*time.Hour)
		}

		// Block if quota exceeded
		if count > int64(limit) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "Daily deep-dive quota exceeded. Upgrade to Premium for unlimited analyses.",
			})
			return
		}

		c.Next()
	}
}
