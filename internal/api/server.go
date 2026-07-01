// Package api wires the HTTP router and all handlers.
package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/redis/go-redis/v9"

	vaultnuban "github.com/systynlabs/vaultnuban"
	"github.com/systynlabs/vaultnuban/internal/api/handlers"
	"github.com/systynlabs/vaultnuban/internal/api/middleware"
	"github.com/systynlabs/vaultnuban/internal/config"
	"github.com/systynlabs/vaultnuban/internal/provider"
	"github.com/systynlabs/vaultnuban/internal/recon"
	"github.com/systynlabs/vaultnuban/internal/relay"
	"github.com/systynlabs/vaultnuban/internal/service"
	"github.com/systynlabs/vaultnuban/internal/store"
)

// Dependencies groups every external dependency the API needs.
type Dependencies struct {
	TenantStore     store.TenantStore
	HealthStore     store.PlatformHealthStore
	AuditStore      store.AuditStore
	AuthStore       store.AuthStore
	WebhookStore    store.WebhookEventStore
	CustomerStore   store.CustomerStore
	TxnStore        store.TransactionStore
	VAStore         store.VirtualAccountStore
	SuspenseStore   store.SuspenseStore
	WithdrawalStore store.WithdrawalStore
	RelayStore      store.RelayStore
	SweepStore      store.SweepStore
	SettingsStore   store.SettingsStore
	TierLimits      *config.TierLimitsCache
	Redis           *redis.Client
	CustomerSvc     *service.CustomerService
	Provisioning    *service.ProvisioningService
	SuspenseSvc     *service.SuspenseService
	WithdrawalSvc   *service.WithdrawalService
	CollectionSvc   *service.CollectionService
	CollectionStore store.CollectionStore
	Provider        provider.Provider
	Worker          *recon.Worker
	Sweep           *recon.SweepRunner
	Dispatcher      *relay.Dispatcher
	SweepToken      string
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
	r.Get("/", handleReadme)

	// Human auth endpoints — no tenant auth
	authH := handlers.NewAuthHandler(deps.AuthStore, deps.TenantStore, deps.SweepToken)
	r.Post("/auth/login", authH.Login)

	// Initialise handlers
	customerH := handlers.NewCustomerHandler(deps.CustomerSvc, deps.CustomerStore)
	vaH := handlers.NewVAHandler(deps.Provisioning, deps.VAStore)
	webhookH := handlers.NewWebhookHandler(deps.Provider, deps.WebhookStore, deps.Worker)
	sweepH := handlers.NewSweepHandler(deps.Sweep, deps.SweepToken)
	txnH := handlers.NewTransactionHandler(deps.TxnStore, deps.VAStore, deps.CustomerStore)
	suspenseH := handlers.NewSuspenseHandler(deps.SuspenseSvc)
	withdrawalH := handlers.NewWithdrawalHandler(deps.WithdrawalSvc, deps.WithdrawalStore)
	collectionH := handlers.NewCollectionHandler(deps.CollectionSvc)
	relayH := handlers.NewRelayHandler(deps.RelayStore, deps.Dispatcher)
	settingsH := handlers.NewSettingsHandler(deps.SettingsStore, deps.TierLimits)
	healthH := handlers.NewHealthHandler(deps.HealthStore, deps.SweepStore, deps.VAStore, deps.Provider)
	auditH := handlers.NewAuditHandler(deps.AuditStore)
	apiKeyH := handlers.NewAPIKeyHandler(deps.TenantStore)
	internalOpsH := handlers.NewInternalOpsHandler(
		deps.WebhookStore, deps.SuspenseStore, deps.TxnStore, deps.VAStore,
		deps.CustomerStore, deps.SuspenseSvc, deps.Provider,
	)

	// Authenticated tenant API
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth(deps.TenantStore))
		r.Use(middleware.Idempotency(deps.Redis))

		r.Route("/v1", func(r chi.Router) {
			// Customer management
			r.Get("/customers", customerH.ListCustomers)
			r.Post("/customers", customerH.CreateCustomer)

			// Tenant-level flat lists (for dashboard)
			r.Get("/virtual-accounts", vaH.ListVAs)
			r.Get("/transactions", txnH.ListTenantTransactions)

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
				r.Get("/balance", txnH.GetBalance)

				// Withdrawals (outbound transfers)
				r.Post("/withdrawals", withdrawalH.Initiate)
				r.Get("/withdrawals", withdrawalH.List)

				// Collections (dynamic payment requests)
				r.Post("/collections", collectionH.Create)
				r.Get("/collections", collectionH.List)
				r.Get("/collections/{collectionID}", collectionH.Get)
				r.Delete("/collections/{collectionID}", collectionH.Cancel)
			})

			// Payee account resolution
			r.Get("/payees/resolve", withdrawalH.ResolvePayee)

			// Suspense (Phase 6)
			r.Get("/suspense", suspenseH.ListSuspense)
			r.Patch("/suspense/{itemID}", suspenseH.ResolveSuspense)

			// Single transaction lookup
			r.Get("/transactions/{transactionID}", txnH.GetSingleTransaction)

			// Webhook relay (FR-11)
			r.Post("/webhook-endpoints", relayH.CreateEndpoint)
			r.Get("/webhook-endpoints", relayH.ListEndpoints)
			r.Get("/webhook-deliveries", relayH.ListDeliveries)
			r.Post("/webhook-deliveries/{deliveryID}/replays", relayH.ReplayDelivery)

			// Audit log
			r.Get("/audit", auditH.ListAuditEntries)

			// API key self-service
			r.Get("/api-keys", apiKeyH.ListAPIKeys)
			r.Post("/api-keys", apiKeyH.CreateAPIKey)
			r.Delete("/api-keys/{keyID}", apiKeyH.RevokeAPIKey)
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
		r.Post("/internal/tenants", authH.Onboard)
		r.Post("/internal/admins", authH.OnboardAdmin)
		r.Get("/internal/tenants", authH.ListTenants)
		r.Get("/internal/health", healthH.GetPlatformHealth)
		r.Get("/internal/sweep-runs", healthH.ListSweepRuns)
		r.Get("/internal/suspense", healthH.ListCrossTenantSuspense)
		r.Get("/internal/virtual-accounts", healthH.ListAllVAs)
		r.Get("/internal/nomba-virtual-accounts", healthH.ListNombaVAs)
		r.Get("/internal/webhook-events", internalOpsH.ListWebhookEvents)
		r.Post("/internal/reprocess-suspense", internalOpsH.ReprocessSuspense)
	})

	return r
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// handleReadme serves the repo's README at the API root, rendered as
// GitHub-styled HTML, so visitors hitting the bare domain see project docs
// instead of a 404. Sending `Accept: text/markdown` returns the raw source.
func handleReadme(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.Header.Get("Accept"), "text/markdown") {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(vaultnuban.ReadmeMD))
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(readmePageHead))
	_, _ = w.Write([]byte(vaultnuban.ReadmeHTML()))
	_, _ = w.Write([]byte(readmePageTail))
}

const readmePageHead = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>VaultNUBAN API</title>
<style>
  :root {
    color-scheme: light dark;
    --fg: #1f2328; --bg: #ffffff; --muted: #59636e; --border: #d1d9e0;
    --code-bg: #f6f8fa; --link: #0969da; --accent: #d4a72c;
  }
  @media (prefers-color-scheme: dark) {
    :root { --fg: #e6edf3; --bg: #0d1117; --muted: #8d96a0; --border: #30363d; --code-bg: #161b22; --link: #4493f8; }
  }
  * { box-sizing: border-box; }
  body {
    max-width: 860px; margin: 0 auto; padding: 3rem 1.5rem 5rem;
    background: var(--bg); color: var(--fg);
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
    line-height: 1.65; font-size: 16px;
  }
  .markdown-body h1, .markdown-body h2, .markdown-body h3, .markdown-body h4 {
    font-weight: 600; line-height: 1.25; margin: 1.8em 0 0.8em;
  }
  .markdown-body h1 { font-size: 2rem; padding-bottom: 0.4em; border-bottom: 1px solid var(--border); }
  .markdown-body h2 { font-size: 1.5rem; padding-bottom: 0.35em; border-bottom: 1px solid var(--border); }
  .markdown-body h3 { font-size: 1.2rem; }
  .markdown-body h1:first-child { margin-top: 0; }
  .markdown-body p, .markdown-body ul, .markdown-body ol, .markdown-body blockquote, .markdown-body table { margin: 0.8em 0; }
  .markdown-body ul, .markdown-body ol { padding-left: 1.8em; }
  .markdown-body li + li { margin-top: 0.2em; }
  .markdown-body a { color: var(--link); text-decoration: none; }
  .markdown-body a:hover { text-decoration: underline; }
  .markdown-body code {
    background: var(--code-bg); border-radius: 4px; padding: 0.15em 0.4em;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 0.88em;
  }
  .markdown-body pre {
    background: var(--code-bg); border: 1px solid var(--border); border-radius: 8px;
    padding: 1em; overflow-x: auto; line-height: 1.5;
  }
  .markdown-body pre code { background: none; padding: 0; font-size: 0.85em; }
  .markdown-body blockquote {
    border-left: 4px solid var(--border); margin-left: 0; padding: 0 1em; color: var(--muted);
  }
  .markdown-body table { border-collapse: collapse; display: block; overflow-x: auto; width: max-content; max-width: 100%; }
  .markdown-body th, .markdown-body td { border: 1px solid var(--border); padding: 0.5em 0.9em; }
  .markdown-body th { background: var(--code-bg); font-weight: 600; }
  .markdown-body tr:nth-child(2n) { background: var(--code-bg); }
  .markdown-body img { max-width: 100%; }
  .markdown-body hr { border: none; border-top: 1px solid var(--border); margin: 2em 0; }
  .readme-footer {
    margin-top: 3em; padding-top: 1.5em; border-top: 1px solid var(--border);
    color: var(--muted); font-size: 0.85rem;
  }
  .readme-footer a { color: var(--link); text-decoration: none; }
  .readme-footer a:hover { text-decoration: underline; }
</style>
</head>
<body>
<article class="markdown-body">
`

const readmePageTail = `
</article>
<p class="readme-footer">
  Serving <code>README.md</code> from the running build &middot;
  <a href="https://github.com/Systyn-Labs/vaultnuban">Source</a>
</p>
</body>
</html>`

func notImplemented(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_, _ = w.Write([]byte(`{"status":"not implemented"}`))
}
