// Package api wires the HTTP router and all handlers.
package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/redis/go-redis/v9"

	"github.com/systynlabs/vaultnuban/internal/api/handlers"
	"github.com/systynlabs/vaultnuban/internal/api/middleware"
	"github.com/systynlabs/vaultnuban/internal/config"
	"github.com/systynlabs/vaultnuban/internal/provider"
	"github.com/systynlabs/vaultnuban/internal/recon"
	"github.com/systynlabs/vaultnuban/internal/service"
	"github.com/systynlabs/vaultnuban/internal/store"
)

// Dependencies groups every external dependency the API needs.
type Dependencies struct {
	TenantStore   store.TenantStore
	AuthStore     store.AuthStore
	WebhookStore  store.WebhookEventStore
	CustomerStore store.CustomerStore
	TxnStore      store.TransactionStore
	VAStore       store.VirtualAccountStore
	RelayStore    store.RelayStore
	SettingsStore store.SettingsStore
	TierLimits    *config.TierLimitsCache
	Redis         *redis.Client
	CustomerSvc   *service.CustomerService
	Provisioning  *service.ProvisioningService
	SuspenseSvc   *service.SuspenseService
	Provider      provider.Provider
	Worker        *recon.Worker
	Sweep         *recon.SweepRunner
	SweepToken    string
}

// NewRouter builds and returns the fully configured chi router.
func NewRouter(deps Dependencies) http.Handler {
	r := chi.NewRouter()

	// Global middleware
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"https://vaultnuban-client.pages.dev", "http://localhost:5173", "http://localhost:4173"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "HEAD"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Idempotency-Key", "X-Request-ID"},
		ExposedHeaders:   []string{"X-Request-ID"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	// Infra endpoints — no auth
	r.Get("/healthz", handleHealthz)

	// Human auth endpoints — no tenant auth
	authH := handlers.NewAuthHandler(deps.AuthStore, deps.TenantStore)
	r.Post("/auth/login", authH.Login)

	// Initialise handlers
	customerH := handlers.NewCustomerHandler(deps.CustomerSvc, deps.CustomerStore)
	vaH := handlers.NewVAHandler(deps.Provisioning)
	webhookH := handlers.NewWebhookHandler(deps.Provider, deps.WebhookStore, deps.Worker)
	sweepH := handlers.NewSweepHandler(deps.Sweep, deps.SweepToken)
	txnH := handlers.NewTransactionHandler(deps.TxnStore, deps.VAStore, deps.CustomerStore)
	suspenseH := handlers.NewSuspenseHandler(deps.SuspenseSvc)
	relayH := handlers.NewRelayHandler(deps.RelayStore)
	settingsH := handlers.NewSettingsHandler(deps.SettingsStore, deps.TierLimits)

	// Authenticated tenant API
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth(deps.TenantStore))
		r.Use(middleware.Idempotency(deps.Redis))

		r.Route("/v1", func(r chi.Router) {
			// Customer management
			r.Get("/customers", customerH.ListCustomers)
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
				r.Get("/transactions", txnH.ListTransactions)
				r.Get("/statement", txnH.GetStatement)
			})

			// Suspense (Phase 6)
			r.Get("/suspense", suspenseH.ListSuspense)
			r.Post("/suspense/{itemID}/resolve", suspenseH.ResolveSuspense)

			// Webhook relay registration (FR-11)
			r.Post("/webhook-endpoints", relayH.CreateEndpoint)
		})
	})

	// Nomba webhook — no tenant auth, HMAC-verified inside the handler (FR-4)
	r.Post("/webhooks/nomba", webhookH.HandleNombaWebhook)

	// Internal cron endpoint — authenticated via INTERNAL_SWEEP_TOKEN (FR-6).
	r.Get("/internal/sweep", sweepH.HandleSweep)
	r.Head("/internal/sweep", sweepH.HandleSweep)

	// Admin settings + onboarding — protected by INTERNAL_SWEEP_TOKEN
	r.Group(func(r chi.Router) {
		r.Use(middleware.SweepTokenAuth(deps.SweepToken))
		r.Get("/internal/settings/tier-limits", settingsH.GetTierLimits)
		r.Put("/internal/settings/tier-limits", settingsH.PutTierLimits)
		r.Post("/internal/onboard", authH.Onboard)
		r.Post("/internal/onboard-admin", authH.OnboardAdmin)
	})

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
