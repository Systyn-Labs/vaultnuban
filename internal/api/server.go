// Package api wires the HTTP router and all handlers.
package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/redis/go-redis/v9"

	"github.com/systynlabs/vaultnuban/internal/api/handlers"
	"github.com/systynlabs/vaultnuban/internal/api/middleware"
	"github.com/systynlabs/vaultnuban/internal/provider"
	"github.com/systynlabs/vaultnuban/internal/recon"
	"github.com/systynlabs/vaultnuban/internal/service"
	"github.com/systynlabs/vaultnuban/internal/store"
)

// Dependencies groups every external dependency the API needs.
type Dependencies struct {
	TenantStore  store.TenantStore
	WebhookStore store.WebhookEventStore
	Redis        *redis.Client
	CustomerSvc  *service.CustomerService
	Provisioning *service.ProvisioningService
	Provider     provider.Provider
	Worker       *recon.Worker
}

// NewRouter builds and returns the fully configured chi router.
func NewRouter(deps Dependencies) http.Handler {
	r := chi.NewRouter()

	// Global middleware
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)

	// Infra endpoints — no auth
	r.Get("/healthz", handleHealthz)

	// Initialise handlers
	customerH := handlers.NewCustomerHandler(deps.CustomerSvc)
	vaH := handlers.NewVAHandler(deps.Provisioning)
	webhookH := handlers.NewWebhookHandler(deps.Provider, deps.WebhookStore, deps.Worker)

	// Authenticated tenant API
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth(deps.TenantStore))
		r.Use(middleware.Idempotency(deps.Redis))

		r.Route("/v1", func(r chi.Router) {
			// Customer management
			r.Post("/customers", customerH.CreateCustomer)

			r.Route("/customers/{customerID}", func(r chi.Router) {
				// Identity / KYC
				r.Patch("/identity", customerH.UpdateKYCTier)

				// Virtual account lifecycle
				r.Post("/virtual-account", vaH.ProvisionVA)
				r.Get("/virtual-account", vaH.GetVA)
				r.Patch("/virtual-account", vaH.PatchVA)
				r.Delete("/virtual-account", vaH.DeleteVA)

				// Transactions (Phase 6)
				r.Get("/transactions", notImplemented)
				r.Get("/statement", notImplemented)
			})

			// Suspense (Phase 6)
			r.Get("/suspense", notImplemented)
			r.Post("/suspense/{itemID}/resolve", notImplemented)

			// Webhook relay registration (P1 / Phase 10)
			r.Post("/webhook-endpoints", notImplemented)
		})
	})

	// Nomba webhook — no tenant auth, HMAC-verified inside the handler (FR-4)
	r.Post("/webhooks/nomba", webhookH.HandleNombaWebhook)

	// Internal cron endpoint — authenticated via INTERNAL_SWEEP_TOKEN (Phase 5)
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
