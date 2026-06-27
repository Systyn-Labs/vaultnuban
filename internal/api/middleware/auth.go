// Package middleware contains HTTP middleware for the VaultNUBAN API.
package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/systynlabs/vaultnuban/internal/api/problem"
	"github.com/systynlabs/vaultnuban/internal/domain"
	"github.com/systynlabs/vaultnuban/internal/store"
)

type contextKey string

const (
	tenantKey contextKey = "tenant"
	apiKeyKey contextKey = "api_key"
)

// Auth validates the Authorization: Bearer sk_… header, looks up the API key
// by its SHA-256 hash, and injects the tenant into the request context (FR-1.2).
// Cross-tenant resource access returns 404, not 403 (FR-1.3).
func Auth(ts store.TenantStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := r.Header.Get("Authorization")
			if raw == "" || !strings.HasPrefix(raw, "Bearer ") {
				problem.Unauthorized(w, "missing or malformed Authorization header")
				return
			}

			key := strings.TrimPrefix(raw, "Bearer ")
			hash := hashAPIKey(key)

			tenant, apiKey, err := ts.GetTenantByAPIKey(r.Context(), hash)
			if err != nil || tenant == nil {
				problem.Unauthorized(w, "invalid API key")
				return
			}

			ctx := context.WithValue(r.Context(), tenantKey, tenant)
			ctx = context.WithValue(ctx, apiKeyKey, apiKey)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// TenantFromContext extracts the authenticated tenant from the context.
func TenantFromContext(ctx context.Context) *domain.Tenant {
	t, _ := ctx.Value(tenantKey).(*domain.Tenant)
	return t
}

// APIKeyFromContext extracts the authenticated API key from the context.
func APIKeyFromContext(ctx context.Context) *domain.APIKey {
	k, _ := ctx.Value(apiKeyKey).(*domain.APIKey)
	return k
}

// hashAPIKey returns the hex-encoded SHA-256 of the raw API key.
func hashAPIKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}
