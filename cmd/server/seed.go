package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"

	"golang.org/x/crypto/bcrypt"

	"github.com/systynlabs/vaultnuban/internal/domain"
	"github.com/systynlabs/vaultnuban/internal/logger"
	"github.com/systynlabs/vaultnuban/internal/service"
	"github.com/systynlabs/vaultnuban/internal/store/postgres"
)

// seedDemoData ensures the demo accounts exist on every startup (idempotent).
// It runs after migrations and before the HTTP server starts.
func seedDemoData(
	ctx context.Context,
	authRepo *postgres.AuthRepo,
	tenantRepo *postgres.TenantRepo,
	customerSvc *service.CustomerService,
) {
	// ── Platform admin ────────────────────────────────────────────────────────
	seedCredential(ctx, authRepo, &domain.UserCredential{
		Email:    "operator@systyn.io",
		Name:     "Systyn Operator",
		Role:     "admin",
		TenantID: nil,
	}, "Admin1234!")

	// ── Acme Fintech (demo tenant) ────────────────────────────────────────────
	// Check whether the Acme dev credential already exists to keep this idempotent.
	existing, _, _, err := authRepo.GetCredentialByEmail(ctx, "ada@acme.io")
	if err != nil {
		logger.Errorf("Seed", "check acme tenant: %v", err)
		return
	}
	if existing != nil {
		logger.Log("Seed", "demo accounts already exist — skipping")
		return
	}

	tenant, err := tenantRepo.CreateTenant(ctx, "Acme Fintech")
	if err != nil {
		logger.Errorf("Seed", "create acme tenant: %v", err)
		return
	}

	rawKey, keyHash, keyPrefix, err := newAPIKey()
	if err != nil {
		logger.Errorf("Seed", "generate api key: %v", err)
		return
	}
	if _, err := tenantRepo.CreateAPIKey(ctx, tenant.ID, rawKey, keyHash, keyPrefix); err != nil {
		logger.Errorf("Seed", "create api key: %v", err)
		return
	}
	logger.Logf("Seed", "Acme Fintech tenant created — key_prefix=%s", keyPrefix)

	tid := tenant.ID
	seedCredential(ctx, authRepo, &domain.UserCredential{
		TenantID: &tid, Email: "ada@acme.io", Name: "Adaeze Okonkwo", Role: "dev",
	}, "Dev1234!")
	seedCredential(ctx, authRepo, &domain.UserCredential{
		TenantID: &tid, Email: "bisi@acme.io", Name: "Bisi Thomas", Role: "ops",
	}, "Ops1234!")

	// ── Seed demo customers ───────────────────────────────────────────────────
	nin := func(s string) *string { return &s }
	bvn := func(s string) *string { return &s }

	type demoCustomer struct {
		ref, name string
		identity  domain.IdentityInput
	}
	demos := []demoCustomer{
		{ref: "ada_okonkwo", name: "Adaeze Okonkwo", identity: domain.IdentityInput{NINMasked: nin("****1234"), KYCTier: 2}},
		{ref: "bisi_thomas", name: "Bisi Thomas", identity: domain.IdentityInput{BVNMasked: bvn("****5678"), KYCTier: 3}},
		{ref: "chidi_eze", name: "Chidi Eze", identity: domain.IdentityInput{NINMasked: nin("****9012"), KYCTier: 1}},
		{ref: "dupe_akin", name: "Dupe Akin", identity: domain.IdentityInput{BVNMasked: bvn("****3456"), KYCTier: 2}},
	}
	for _, d := range demos {
		if _, err := customerSvc.CreateCustomer(ctx, tenant.ID, d.ref, d.name, d.identity, "seed"); err != nil {
			logger.Errorf("Seed", "seed customer %s: %v", d.ref, err)
		}
	}

	logger.Log("Seed", "demo accounts and customers seeded successfully")
}

func newAPIKey() (raw, hash, prefix string, err error) {
	b := make([]byte, 16)
	if _, err = rand.Read(b); err != nil {
		return
	}
	hex16 := hex.EncodeToString(b)
	raw = "sk_live_" + hex16
	sum := sha256.Sum256([]byte(raw))
	hash = hex.EncodeToString(sum[:])
	if len(raw) > 16 {
		prefix = raw[:16]
	} else {
		prefix = raw
	}
	return
}

func seedCredential(ctx context.Context, authRepo *postgres.AuthRepo, cred *domain.UserCredential, password string) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		logger.Errorf("Seed", "hash password for %s: %v", cred.Email, err)
		return
	}
	cred.PasswordHash = string(hash)
	if err := authRepo.SeedCredential(ctx, cred); err != nil {
		logger.Errorf("Seed", "seed credential %s: %v", cred.Email, err)
	}
}
