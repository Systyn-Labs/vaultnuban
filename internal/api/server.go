// Package api wires the HTTP router and all handlers.
package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/redis/go-redis/v9"

	"github.com/systynlabs/vaultnuban/internal/api/middleware"
	"github.com/systynlabs/vaultnuban/internal/store"
)

// Dependencies groups every external dependency the API needs.
type Dependencies struct {
	TenantStore store.TenantStore
	Redis       *redis.Client
}

// NewRouter builds and returns the fully configured chi router.
func NewRouter(deps Dependencies) http.Handler {
	r := chi.NewRouter()

	// Global middleware
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)
	r.Use(middleware.RequestID)

	// Infra endpoints — no auth
	r.Get("/healthz", handleHealthz)

	// Authenticated tenant API
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth(deps.TenantStore))
		r.Use(middleware.Idempotency(deps.Redis))

		// Placeholder routes — handlers are added in Phase 3+
		r.Route("/v1", func(r chi.Router) {
			r.Post("/customers", notImplemented)
			r.Get("/customers/{customerID}", notImplemented)
			r.Route("/customers/{customerID}", func(r chi.Router) {
				r.Post("/virtual-account", notImplemented)
				r.Get("/virtual-account", notImplemented)
				r.Patch("/virtual-account", notImplemented)
				r.Delete("/virtual-account", notImplemented)
				r.Patch("/identity", notImplemented)
				r.Get("/transactions", notImplemented)
				r.Get("/statement", notImplemented)
			})
			r.Get("/suspense", notImplemented)
			r.Post("/suspense/{itemID}/resolve", notImplemented)
			r.Post("/webhook-endpoints", notImplemented) // P1
		})
	})

	// Nomba webhook — no tenant auth, HMAC-verified inside the handler
	r.Post("/webhooks/nomba", notImplemented)

	// Internal cron endpoint — authenticated via INTERNAL_SWEEP_TOKEN
	r.Get("/internal/sweep", notImplemented)

	return r
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func notImplemented(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_, _ = w.Write([]byte(`{"status":"not implemented"}`))
}
