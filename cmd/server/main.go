package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/systynlabs/vaultnuban/internal/api"
	"github.com/systynlabs/vaultnuban/internal/config"
	"github.com/systynlabs/vaultnuban/internal/logger"
	"github.com/systynlabs/vaultnuban/internal/provider/nomba"
	"github.com/systynlabs/vaultnuban/internal/recon"
	"github.com/systynlabs/vaultnuban/internal/relay"
	"github.com/systynlabs/vaultnuban/internal/service"
	"github.com/systynlabs/vaultnuban/internal/store"
	"github.com/systynlabs/vaultnuban/internal/store/postgres"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		logger.Errorf("Bootstrap", "config error: %v", err)
		os.Exit(1)
	}

	if logDir := os.Getenv("LOG_DIR"); logDir != "" {
		if err := logger.EnableFileLogging(logDir, 7); err != nil {
			logger.Warnf("Bootstrap", "file logging unavailable: %v", err)
		} else {
			logger.Logf("Bootstrap", "file logging enabled — dir=%s retain=7days", logDir)
		}
	}

	logger.Logf("Bootstrap", "starting — env=%s port=%s base_url=%s",
		cfg.Env, cfg.Port, cfg.NombaBaseURL)

	// ── Postgres ──────────────────────────────────────────────────────────────
	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Errorf("Bootstrap", "database error: %v", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := store.RunMigrations(ctx, pool); err != nil {
		logger.Errorf("Bootstrap", "migration error: %v", err)
		os.Exit(1)
	}
	logger.Log("MigrationRunner", "all migrations applied successfully")

	// ── Redis ─────────────────────────────────────────────────────────────────
	rdb, err := store.OpenRedis(ctx, cfg.RedisURL)
	if err != nil {
		logger.Errorf("Bootstrap", "redis error: %v", err)
		os.Exit(1)
	}
	defer rdb.Close()
	logger.Log("RedisClient", "connection established")

	// ── Repos ─────────────────────────────────────────────────────────────────
	tenantRepo := postgres.NewTenantRepo(pool)
	healthRepo := postgres.NewHealthRepo(pool)
	authRepo := postgres.NewAuthRepo(pool)
	customerRepo := postgres.NewCustomerRepo(pool)
	vaRepo := postgres.NewVARepo(pool)
	auditRepo := postgres.NewAuditRepo(pool)
	txnRepo := postgres.NewTransactionRepo(pool)
	webhookRepo := postgres.NewWebhookRepo(pool)
	suspenseRepo := postgres.NewSuspenseRepo(pool)
	withdrawalRepo := postgres.NewWithdrawalRepo(pool)
	collectionRepo := postgres.NewCollectionRepo(pool)
	sweepRepo := postgres.NewSweepRepo(pool)
	relayRepo := postgres.NewRelayRepo(pool)
	settingsRepo := postgres.NewSettingsRepo(pool)

	// ── Tier limits: seed defaults on first run, then load into cache ─────────
	tierLimits := config.NewTierLimitsCache()
	if err := settingsRepo.SeedSetting(ctx, config.TierLimitsKey, config.DefaultTierLimitsJSON); err != nil {
		logger.Errorf("Bootstrap", "settings seed error: %v", err)
		os.Exit(1)
	}
	raw, err := settingsRepo.GetSetting(ctx, config.TierLimitsKey)
	if err != nil {
		logger.Errorf("Bootstrap", "settings load error: %v", err)
		os.Exit(1)
	}
	if raw == nil {
		raw = config.DefaultTierLimitsJSON
	}
	if err := tierLimits.Load(raw); err != nil {
		logger.Errorf("Bootstrap", "tier limits parse error: %v", err)
		os.Exit(1)
	}
	logger.Log("Bootstrap", "tier limits loaded from database")

	// ── Provider ──────────────────────────────────────────────────────────────
	prov := nomba.New(
		cfg.NombaBaseURL,
		cfg.NombaClientID,
		cfg.NombaClientSecret,
		cfg.NombaAccountID,
		cfg.NombaWebhookSecret,
	)
	logger.Logf("NombaProvider", "configured — base_url=%s", cfg.NombaBaseURL)

	// ── Services ──────────────────────────────────────────────────────────────
	customerSvc := service.NewCustomerService(customerRepo, auditRepo)

	// ── Seed demo accounts (idempotent — no-op on subsequent startups) ────────
	seedDemoData(ctx, authRepo, tenantRepo, customerSvc)
	provisioningSvc := service.NewProvisioningService(customerRepo, vaRepo, auditRepo, prov)
	suspenseSvc := service.NewSuspenseService(suspenseRepo, txnRepo, customerRepo, vaRepo, auditRepo)
	withdrawalSvc := service.NewWithdrawalService(withdrawalRepo, txnRepo, customerRepo, vaRepo, prov)
	collectionSvc := service.NewCollectionService(collectionRepo, customerRepo, vaRepo)

	// ── Reconciliation worker + sweep ─────────────────────────────────────────
	matcher := recon.NewMatcher(vaRepo, txnRepo, tierLimits)
	worker := recon.NewWorker(512, matcher, txnRepo, webhookRepo, suspenseRepo, customerRepo)

	// ── Relay dispatcher (FR-11) ───────────────────────────────────────────────
	dispatcher := relay.NewDispatcher(relayRepo)
	worker.SetDispatcher(dispatcher)
	worker.SetCollectionService(collectionSvc)

	sweepRunner := recon.NewSweepRunner(prov, txnRepo, sweepRepo, worker, cfg.SweepInterval, cfg.SweepOverlap)

	go worker.Run(ctx)

	logger.Logf("SweepRunner", "configured — interval=%s overlap=%s",
		cfg.SweepInterval, cfg.SweepOverlap)

	// ── HTTP server ───────────────────────────────────────────────────────────
	deps := api.Dependencies{
		Dispatcher: dispatcher,
		TenantStore:   tenantRepo,
		HealthStore:   healthRepo,
		AuditStore:    auditRepo,
		AuthStore:     authRepo,
		WebhookStore:  webhookRepo,
		CustomerStore: customerRepo,
		TxnStore:      txnRepo,
		VAStore:       vaRepo,
		SuspenseStore:   suspenseRepo,
		WithdrawalStore: withdrawalRepo,
		RelayStore:    relayRepo,
		SweepStore:    sweepRepo,
		SettingsStore: settingsRepo,
		TierLimits:    tierLimits,
		Redis:         rdb,
		CustomerSvc:   customerSvc,
		Provisioning:  provisioningSvc,
		SuspenseSvc:   suspenseSvc,
		WithdrawalSvc:   withdrawalSvc,
		CollectionSvc:   collectionSvc,
		CollectionStore: collectionRepo,
		Provider:      prov,
		Worker:        worker,
		Sweep:         sweepRunner,
		SweepToken:    cfg.InternalSweepToken,
	}

	router := api.NewRouter(deps)
	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      router,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		logger.Logf("RouterExplorer", "listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("HttpServer", "fatal: %v", err)
		}
	}()

	logger.Log("VaultNUBAN", "application started successfully")

	<-quit
	logger.Warn("VaultNUBAN", "shutdown signal received")
	cancel()

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		logger.Errorf("HttpServer", "shutdown error: %v", err)
	}
	logger.Log("VaultNUBAN", "application stopped")
	fmt.Println()
}
