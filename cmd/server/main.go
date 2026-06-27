package main

import (
	"context"
	"fmt"
	"log"
	"os"

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

	fmt.Println("Migrations applied. Ready.")
}
