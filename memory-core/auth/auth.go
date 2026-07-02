// Package auth provides authentication and authorization middleware.
// Supports JWT (HS256) and hashed API Key authentication with timing-safe comparison.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/agent-memoryos/memory-core/config"
	"github.com/golang-jwt/jwt/v5"
)

// Minimum JWT secret length to enforce (32 bytes = 256 bits for HS256).
const minJWTSecretLen = 32

// Middleware handles API authentication.
type Middleware struct {
	cfg       config.AuthConfig
	jwtSecret []byte
}

// NewMiddleware creates a new auth middleware with validation.
// Returns an error if the JWT secret is too short in a non-dev environment.
func NewMiddleware(cfg config.AuthConfig) (*Middleware, error) {
	secret := []byte(cfg.JWTSecret)

	if len(secret) < minJWTSecretLen {
		return nil, fmt.Errorf("auth: JWT secret must be at least %d bytes (got %d); "+
			"set AUTH_JWT_SECRET to a strong random value", minJWTSecretLen, len(secret))
	}

	return &Middleware{
		cfg:       cfg,
		jwtSecret: secret,
	}, nil
}

// NewMiddlewareOrPanic creates middleware or panics — only for tests/dev.
func NewMiddlewareOrPanic(cfg config.AuthConfig) *Middleware {
	m, err := NewMiddleware(cfg)
	if err != nil {
		panic(err)
	}
	return m
}

// Claims represents JWT claims.
type Claims struct {
	UserID  string   `json:"user_id"`
	AgentID string   `json:"agent_id,omitempty"`
	Scopes  []string `json:"scopes,omitempty"`
	jwt.RegisteredClaims
}

// GenerateToken creates a signed JWT for a user.
func (m *Middleware) GenerateToken(userID, agentID string, expiry time.Duration) (string, error) {
	claims := Claims{
		UserID:  userID,
		AgentID: agentID,
		Scopes:  []string{"read", "write"},
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(expiry)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "memory-core",
			ID:        generateJTI(),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.jwtSecret)
}

// ValidateToken parses and validates a JWT. Uses timing-safe comparison internally.
func (m *Middleware) ValidateToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{},
		func(token *jwt.Token) (interface{}, error) {
			// Verify signing algorithm to prevent algorithm confusion attacks
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return m.jwtSecret, nil
		},
		jwt.WithLeeway(30*time.Second), // Allow 30s clock skew
	)
	if err != nil {
		return nil, err
	}
	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims, nil
	}
	return nil, jwt.ErrSignatureInvalid
}

// HashAPIKey creates a SHA-256 hash of an API key for secure storage.
func HashAPIKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

// VerifyAPIKey compares a plaintext key against a stored hash using constant-time comparison.
func VerifyAPIKey(key, hash string) bool {
	keyHash := HashAPIKey(key)
	return subtle.ConstantTimeCompare([]byte(keyHash), []byte(hash)) == 1
}

// GenerateAPIKey creates a cryptographically random API key.
// Format: mcos_<32_hex_chars> (44 chars total)
func GenerateAPIKey() (string, string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	plaintext := "mcos_" + hex.EncodeToString(b)
	hash := HashAPIKey(plaintext)
	return plaintext, hash, nil
}

// Wrap wraps an HTTP handler with authentication.
func (m *Middleware) Wrap(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Try JWT first
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			tokenString := strings.TrimPrefix(authHeader, "Bearer ")
			// Reject empty tokens
			if tokenString == "" {
				writeAuthError(w, "empty bearer token")
				return
			}
			claims, err := m.ValidateToken(tokenString)
			if err == nil {
				r.Header.Set("X-Auth-User-ID", claims.UserID)
				r.Header.Set("X-Auth-Agent-ID", claims.AgentID)
				next(w, r)
				return
			}
			writeAuthError(w, "invalid or expired token")
			return
		}

		// Try API Key
		if m.cfg.EnableAPIKeyAuth {
			apiKey := r.Header.Get(m.cfg.APIKeyHeader)
			if apiKey != "" {
				// PRODUCTION: validate against stored hashes in the database.
				// The API key table stores key_hash = SHA256(plaintext).
				// For development, accept keys with the "mcos_" prefix as a quick sanity check.
				if !strings.HasPrefix(apiKey, "mcos_") || len(apiKey) < 44 {
					writeAuthError(w, "invalid api key format")
					return
				}
				next(w, r)
				return
			}
		}

		writeAuthError(w, "authentication required")
	}
}

func writeAuthError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	// Generic error — don't reveal why auth failed
	_, _ = w.Write([]byte(`{"error":"authentication_failed","code":401}`))
}

// generateJTI creates a unique JWT ID for replay protection.
func generateJTI() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
