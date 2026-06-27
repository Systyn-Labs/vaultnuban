package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/systynlabs/vaultnuban/internal/api"
	"github.com/systynlabs/vaultnuban/internal/config"
	"github.com/systynlabs/vaultnuban/internal/provider/nomba"
	"github.com/systynlabs/vaultnuban/internal/recon"
	"github.com/systynlabs/vaultnuban/internal/service"
	"github.com/systynlabs/vaultnuban/internal/store"
	"github.com/systynlabs/vaultnuban/internal/store/postgres"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		log.Printf("config error: %v", err)
		os.Exit(1)
	}

	fmt.Printf("VaultNUBAN starting — env=%s port=%s base_url=%s\n",
		cfg.Env, cfg.Port, cfg.NombaBaseURL)

	// ── Postgres ──────────────────────────────────────────────────────────────
	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Printf("database error: %v", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := store.RunMigrations(ctx, pool); err != nil {
		log.Printf("migration error: %v", err)
		os.Exit(1)
	}
	log.Println("migrations applied")

	// ── Redis ─────────────────────────────────────────────────────────────────
	rdb, err := store.OpenRedis(ctx, cfg.RedisURL)
	if err != nil {
		log.Printf("redis error: %v", err)
		os.Exit(1)
	}
	defer rdb.Close()
	log.Println("redis connected")

	// ── Repos ─────────────────────────────────────────────────────────────────
	tenantRepo := postgres.NewTenantRepo(pool)
	customerRepo := postgres.NewCustomerRepo(pool)
	vaRepo := postgres.NewVARepo(pool)
	auditRepo := postgres.NewAuditRepo(pool)
	txnRepo := postgres.NewTransactionRepo(pool)
	webhookRepo := postgres.NewWebhookRepo(pool)
	suspenseRepo := postgres.NewSuspenseRepo(pool)

	// ── Provider ──────────────────────────────────────────────────────────────
	prov := nomba.New(
		cfg.NombaBaseURL,
		cfg.NombaClientID,
		cfg.NombaClientSecret,
		cfg.NombaAccountID,
		cfg.NombaWebhookSecret,
	)

	// ── Services ──────────────────────────────────────────────────────────────
	customerSvc := service.NewCustomerService(customerRepo, auditRepo)
	provisioningSvc := service.NewProvisioningService(customerRepo, vaRepo, auditRepo, prov)

	// ── Reconciliation worker ─────────────────────────────────────────────────
	matcher := recon.NewMatcher(vaRepo, txnRepo, cfg.TierLimits)
	worker := recon.NewWorker(512, matcher, txnRepo, webhookRepo, suspenseRepo, customerRepo)

	go worker.Run(ctx)

	// ── HTTP server ───────────────────────────────────────────────────────────
	deps := api.Dependencies{
		TenantStore:  tenantRepo,
		WebhookStore: webhookRepo,
		Redis:        rdb,
		CustomerSvc:  customerSvc,
		Provisioning: provisioningSvc,
		Provider:     prov,
		Worker:       worker,
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
		log.Printf("listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("server error: %v", err)
		}
	}()

	<-quit
	log.Println("shutting down...")
	cancel() // stop worker

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
	log.Println("stopped")
}
