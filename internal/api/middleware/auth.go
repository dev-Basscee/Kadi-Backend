package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

const userIDKey = "userID"

// Claims represents the payload inside a Supabase-issued JWT.
// Supabase follows the standard JWT structure with a "sub" field for the user UUID.
type Claims struct {
	jwt.RegisteredClaims
	Role  string `json:"role"`
	Email string `json:"email"`
}

// Middleware returns a Gin handler that validates the Supabase JWT on every request.
// MOCKED FOR LOCAL UI TESTING: Always injects a mock UUID.
func Middleware(jwtSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		token, err := extractBearer(authHeader)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
			return
		}

		claims, err := verifyToken(token, jwtSecret)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token: " + err.Error()})
			return
		}

		c.Set(userIDKey, claims.Subject)
		c.Set("isPremium", claims.Role == "premium" || claims.Role == "admin")

		c.Next()
	}
}

// GetUserID retrieves the authenticated user UUID from the Gin context.
// Always call this inside a handler that is protected by Middleware.
func GetUserID(c *gin.Context) string {
	v, _ := c.Get(userIDKey)
	id, _ := v.(string)
	return id
}

// ─── internals ──────────────────────────────────────────────────────────────

func extractBearer(header string) (string, error) {
	if header == "" {
		return "", errors.New("authorization header is empty")
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", errors.New("authorization header must be Bearer <token>")
	}
	return parts[1], nil
}

func verifyToken(tokenStr, secret string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		// Supabase signs with HS256
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("token claims are invalid")
	}
	return claims, nil
}
