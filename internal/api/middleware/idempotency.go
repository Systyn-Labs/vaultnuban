package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/systynlabs/vaultnuban/internal/api/problem"
)

const (
	idemKeyTTL    = 24 * time.Hour
	idemKeyPrefix = "idem:"
	inFlight      = "__inflight__"
)

// storedResponse is the value persisted in Redis once a request completes.
type storedResponse struct {
	StatusCode int         `json:"status_code"`
	Body       json.RawMessage `json:"body"`
}

// responseRecorder captures the status code and body written by a handler.
type responseRecorder struct {
	http.ResponseWriter
	statusCode int
	buf        bytes.Buffer
}

func (r *responseRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	r.buf.Write(b)
	return r.ResponseWriter.Write(b)
}

// Idempotency implements FR-9.1.
// First call: SETNX the key as in-flight; run the handler; store the response.
// Retry with same key: replay the stored response.
// Concurrent in-flight duplicate: 409.
func Idempotency(rdb *redis.Client) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			idemKey := r.Header.Get("Idempotency-Key")
			if idemKey == "" {
				next.ServeHTTP(w, r)
				return
			}

			tenant := TenantFromContext(r.Context())
			if tenant == nil {
				next.ServeHTTP(w, r)
				return
			}

			redisKey := idemKeyPrefix + tenant.ID + ":" + idemKey
			ctx := r.Context()

			// Try to read an existing entry.
			existing, err := rdb.Get(ctx, redisKey).Result()
			if err == nil {
				if existing == inFlight {
					problem.Conflict(w, "a request with this Idempotency-Key is already in progress")
					return
				}
				// Replay stored response.
				var stored storedResponse
				if jsonErr := json.Unmarshal([]byte(existing), &stored); jsonErr == nil {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Idempotency-Replayed", "true")
					w.WriteHeader(stored.StatusCode)
					_, _ = w.Write(stored.Body)
					return
				}
			}

			// Mark in-flight (NX = only if not exists, short TTL to avoid stuck locks).
			set, err := rdb.SetNX(ctx, redisKey, inFlight, 30*time.Second).Result()
			if err != nil || !set {
				// Someone else beat us to it or Redis error — treat as in-flight.
				problem.Conflict(w, "a request with this Idempotency-Key is already in progress")
				return
			}

			rec := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}
			next.ServeHTTP(rec, r)

			// Persist the completed response for replays.
			stored := storedResponse{
				StatusCode: rec.statusCode,
				Body:       json.RawMessage(rec.buf.Bytes()),
			}
			if b, err := json.Marshal(stored); err == nil {
				// Use a background context so a cancelled request doesn't skip persistence.
				_ = rdb.Set(context.Background(), redisKey, b, idemKeyTTL).Err()
			}
		})
	}
}
