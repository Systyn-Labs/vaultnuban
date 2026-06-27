package main

import (
	"fmt"
	"log"
	"os"

	"github.com/systynlabs/vaultnuban/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Printf("config error: %v", err)
		os.Exit(1)
	}

	fmt.Printf("VaultNUBAN starting — env=%s port=%s base_url=%s\n",
		cfg.Env, cfg.Port, cfg.NombaBaseURL)
}
