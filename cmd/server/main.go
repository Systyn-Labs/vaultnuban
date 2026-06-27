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
	"github.com/systynlabs/vaultnuban/internal/store"
)

func main() {
	ctx := context.Background()

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

	// ── HTTP server ───────────────────────────────────────────────────────────
	deps := api.Dependencies{
		Redis: rdb,
		// TenantStore will be wired in Phase 3 when the postgres repo is built.
	}

	router := api.NewRouter(deps)
	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      router,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown
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

	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
	log.Println("stopped")
}
