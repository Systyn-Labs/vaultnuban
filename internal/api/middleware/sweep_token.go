package middleware

import (
	"crypto/subtle"
	"net/http"

	"github.com/systynlabs/vaultnuban/internal/api/problem"
)

// SweepTokenAuth rejects requests whose Authorization header does not match
// "Bearer <token>" using a constant-time comparison.
func SweepTokenAuth(token string) func(http.Handler) http.Handler {
	expected := []byte("Bearer " + token)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), expected) != 1 {
				problem.Unauthorized(w, "invalid internal token")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
