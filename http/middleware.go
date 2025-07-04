package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt"
	"github.com/gorilla/handlers"
)

// context keys
type contextKey int

var ctxKeyJWT contextKey = 1
var ctxKeyEmail contextKey = 2
var ctxKeyTier contextKey = 3

// RateLimiter implements a simple in-memory rate limiter
type RateLimiter struct {
	mu       sync.RWMutex
	requests map[string][]time.Time
	window   time.Duration
	limit    int
}

// NewRateLimiter creates a new rate limiter with the specified window and limit
func NewRateLimiter(window time.Duration, limit int) *RateLimiter {
	return &RateLimiter{
		requests: make(map[string][]time.Time),
		window:   window,
		limit:    limit,
	}
}

// isAllowed checks if a request from the given IP is allowed
func (rl *RateLimiter) isAllowed(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	windowStart := now.Add(-rl.window)

	// Clean up old requests
	requests := rl.requests[ip]
	valid := requests[:0]
	for _, t := range requests {
		if t.After(windowStart) {
			valid = append(valid, t)
		}
	}
	rl.requests[ip] = valid

	// Check if we're under the limit
	if len(valid) >= rl.limit {
		return false
	}

	// Add current request
	rl.requests[ip] = append(rl.requests[ip], now)
	return true
}

// rateLimitMiddleware creates a middleware that applies rate limiting
func rateLimitMiddleware(rl *RateLimiter) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			// Get client IP
			ip := r.RemoteAddr
			if forwardedFor := r.Header.Get("X-Forwarded-For"); forwardedFor != "" {
				ip = strings.Split(forwardedFor, ",")[0]
			}

			// Check if request is allowed
			if !rl.isAllowed(ip) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", fmt.Sprintf("%d", int(rl.window.Seconds())))
				w.WriteHeader(http.StatusTooManyRequests)
				json.NewEncoder(w).Encode(DefaultJSONResponse{
					Error: "rate limit exceeded",
				})
				return
			}

			next(w, r)
		}
	}
}

// Convenience middleware that applies commonly used middleware to the wrapped
// handler. This will make the handler gracefully handle panics, sets the
// content type to application/json, limits the body size that clients can send,
// wraps the handler with the usual CORS settings.
func apiMode(l *slog.Logger, maxBytes int64, headers, methods, origins []string) func(http.HandlerFunc) http.HandlerFunc {
	// Create rate limiter with reasonable defaults
	rl := NewRateLimiter(1*time.Minute, 100) // 100 requests per minute

	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			next = makeGraceful(l)(next)
			next = setMaxBytesReader(maxBytes)(next)
			next = setContentType("application/json")(next)
			next = rateLimitMiddleware(rl)(next) // Uncommented IP rate limiter

			// Apply CORS middleware
			handlers.CORS(
				handlers.AllowedHeaders(headers),
				handlers.AllowedMethods(methods),
				handlers.AllowedOrigins(origins),
			)(next).ServeHTTP(w, r)
		}
	}
}

func setContentType(content string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", content)
			next(w, r)
		}
	}
}

func makeGraceful(l *slog.Logger) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				err := recover()
				if err != nil {
					l.Error("recovered from panic")
					switch v := err.(type) {
					case error:
						writeInternalError(l, w, v)
					case string:
						writeInternalError(l, w, fmt.Errorf("panic error: %s", v))
					default:
						writeInternalError(l, w, fmt.Errorf("recovered but unexpected type from recover()"))
					}
				}
			}()
			next.ServeHTTP(w, r)
		}
	}
}

func setMaxBytesReader(mb int64) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, mb)
			next(w, r)
		}
	}
}

func basicAuthorizerCtxSetEmail(gsk func() string) func(http.ResponseWriter, *http.Request) bool {
	return func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("WWW-Authenticate", `Basic realm="mydomain"`)
		email, pwd, ok := r.BasicAuth()
		if !ok {
			return false
		}
		if email == "" {
			return false
		}
		if pwd != gsk() {
			return false
		}
		ctx := context.WithValue(r.Context(), ctxKeyEmail, email)
		*r = *r.WithContext(ctx)
		return true
	}
}

func bearerAuthorizerCtxSetToken(gsk func() string) func(http.ResponseWriter, *http.Request) bool {
	return func(w http.ResponseWriter, r *http.Request) bool {
		var claims authJWTClaims
		ts := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if ts == "" {
			return false
		}
		kf := func(token *jwt.Token) (interface{}, error) {
			return []byte(gsk()), nil
		}
		token, err := jwt.ParseWithClaims(ts, &claims, kf)
		if err != nil || !token.Valid {
			return false
		}
		ctx := context.WithValue(r.Context(), ctxKeyJWT, token.Claims)
		ctx = context.WithValue(ctx, ctxKeyTier, claims.Tier)
		*r = *r.WithContext(ctx)
		return true
	}
}

// oauthAuthorizerForm creates a middleware that authenticates requests using form data.
// It expects 'username' and 'password' fields in the form.
// It returns true if valid, false otherwise. It does NOT modify context.
func oauthAuthorizerForm(gsk func() string) func(http.ResponseWriter, *http.Request) bool {
	return func(w http.ResponseWriter, r *http.Request) bool {
		if err := r.ParseForm(); err != nil {
			writeUnauthorized(w) // Consider logging the parse error
			return false
		}

		email := r.FormValue("username")
		password := r.FormValue("password")

		if email == "" || password == "" {
			writeUnauthorized(w) // Missing credentials
			return false
		}

		expectedPassword := gsk()
		if expectedPassword == "" {
			slog.Default().Error("oauthAuthorizerForm: server secret key not configured")
			writeInternalError(slog.Default(), w, fmt.Errorf("authentication configuration error"))
			return false
		}

		if password != expectedPassword {
			writeUnauthorized(w) // Invalid credentials
			return false
		}
		ctx := context.WithValue(r.Context(), ctxKeyEmail, email)
		*r = *r.WithContext(ctx)
		return true
	}
}

// Iterates over the supplied authorizers and if at least one passes, then the
// next handler is called, otherwise an unauthorized response is written.
func atLeastOneAuth(authorizers ...func(http.ResponseWriter, *http.Request) bool) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			for _, a := range authorizers {
				if !a(w, r) {
					continue
				}
				next(w, r)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(DefaultJSONResponse{Error: "unauthorized"})
		}
	}
}

// authJWTClaims represents the JWT claims for authentication
type authJWTClaims struct {
	jwt.StandardClaims
	Email  string `json:"email"`
	Status int    `json:"status"`
	Tier   int    `json:"tier"`
}

type DefaultJSONResponse struct {
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

type CreateBountySuccessResponse struct {
	Message  string `json:"message"`
	BountyID string `json:"bounty_id"`
}

func getSecretKey() string {
	return os.Getenv(EnvServerSecretKey)
}

// jwtRateLimitMiddleware creates a middleware that applies rate limiting based on JWT claims
func jwtRateLimitMiddleware(rl *RateLimiter, keyType string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var identifier string

			switch keyType {
			case "email":
				claims, ok := r.Context().Value(ctxKeyJWT).(*authJWTClaims) // Assert type
				if !ok || claims == nil || claims.Email == "" {
					// If JWT claims are missing or email is empty, fall back to IP limiting
					slog.Warn("JWT claims missing or email empty for rate limiting, falling back to IP")
					identifier = r.RemoteAddr // Use IP as fallback key
					if forwardedFor := r.Header.Get("X-Forwarded-For"); forwardedFor != "" {
						identifier = strings.Split(forwardedFor, ",")[0]
					}
				} else {
					identifier = claims.Email // Use email as the key
				}
			default:
				// Default to IP if keyType is not recognized
				slog.Warn("Unrecognized keyType for jwtRateLimitMiddleware, falling back to IP", "keyType", keyType)
				identifier = r.RemoteAddr
				if forwardedFor := r.Header.Get("X-Forwarded-For"); forwardedFor != "" {
					identifier = strings.Split(forwardedFor, ",")[0]
				}
			}

			// Check if request is allowed
			if !rl.isAllowed(identifier) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", fmt.Sprintf("%d", int(rl.window.Seconds())))
				w.WriteHeader(http.StatusTooManyRequests)
				json.NewEncoder(w).Encode(DefaultJSONResponse{
					Error: "rate limit exceeded for this action",
				})
				return
			}

			next(w, r)
		}
	}
}

// requireStatus creates a middleware that checks if the user has the required status level.
func requireStatus(requiredStatus UserStatus) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			claims, ok := r.Context().Value(ctxKeyJWT).(*authJWTClaims) // Extract claims
			if !ok || claims == nil {
				// This should ideally not happen if bearerAuthorizerCtxSetToken ran successfully
				slog.Warn("JWT claims missing in context for status check")
				writeUnauthorized(w) // Treat as unauthorized if claims are missing
				return
			}

			if claims.Status < int(requiredStatus) {
				slog.Info("User status insufficient for endpoint", "email", claims.Email, "user_status", claims.Status, "required_status", requiredStatus)
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(DefaultJSONResponse{Error: "forbidden: insufficient permissions"})
				return
			}

			// User has required status, proceed to the next handler
			next(w, r)
		}
	}
}
