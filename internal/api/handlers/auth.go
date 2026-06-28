package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"

	"golang.org/x/crypto/bcrypt"

	"github.com/systynlabs/vaultnuban/internal/api/problem"
	"github.com/systynlabs/vaultnuban/internal/domain"
	"github.com/systynlabs/vaultnuban/internal/store"
)

// AuthHandler handles human login and tenant onboarding.
type AuthHandler struct {
	auth   store.AuthStore
	tenant store.TenantStore
}

func NewAuthHandler(auth store.AuthStore, tenant store.TenantStore) *AuthHandler {
	return &AuthHandler{auth: auth, tenant: tenant}
}

// Onboard creates a tenant with credentials for its users and a shared API key.
// Protected by INTERNAL_SWEEP_TOKEN — operator only.
func (h *AuthHandler) Onboard(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TenantName string `json:"tenant_name"`
		Users      []struct {
			Name     string `json:"name"`
			Email    string `json:"email"`
			Password string `json:"password"`
			Role     string `json:"role"` // "dev" | "ops"
		} `json:"users"`
	}
	if err := decodeJSON(r, &req); err != nil {
		problem.BadRequest(w, "request body is not valid JSON")
		return
	}
	if req.TenantName == "" || len(req.Users) == 0 {
		problem.BadRequest(w, "tenant_name and at least one user are required")
		return
	}

	ctx := r.Context()

	tenant, err := h.tenant.CreateTenant(ctx, req.TenantName)
	if err != nil {
		problem.InternalServerError(w, "failed to create tenant")
		return
	}

	rawKey, keyHash, keyPrefix, err := generateAPIKey()
	if err != nil {
		problem.InternalServerError(w, "failed to generate API key")
		return
	}
	if _, err := h.tenant.CreateAPIKey(ctx, tenant.ID, rawKey, keyHash, keyPrefix); err != nil {
		problem.InternalServerError(w, "failed to store API key")
		return
	}

	created := 0
	for _, u := range req.Users {
		hash, err := bcrypt.GenerateFromPassword([]byte(u.Password), bcrypt.DefaultCost)
		if err != nil {
			problem.InternalServerError(w, fmt.Sprintf("failed to hash password for %s", u.Email))
			return
		}
		tid := tenant.ID
		cred := &domain.UserCredential{
			TenantID:     &tid,
			Email:        u.Email,
			PasswordHash: string(hash),
			Name:         u.Name,
			Role:         u.Role,
		}
		if err := h.auth.CreateCredential(ctx, cred); err != nil {
			problem.InternalServerError(w, fmt.Sprintf("failed to create credential for %s", u.Email))
			return
		}
		created++
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"tenant_id":     tenant.ID,
		"tenant_name":   tenant.Name,
		"api_key":       rawKey,
		"key_prefix":    keyPrefix,
		"users_created": created,
	})
}

// OnboardAdmin creates a platform admin credential with no tenant.
// Protected by INTERNAL_SWEEP_TOKEN — operator only.
func (h *AuthHandler) OnboardAdmin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		problem.BadRequest(w, "request body is not valid JSON")
		return
	}
	if req.Email == "" || req.Password == "" || req.Name == "" {
		problem.BadRequest(w, "name, email and password are required")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		problem.InternalServerError(w, "failed to hash password")
		return
	}
	cred := &domain.UserCredential{
		TenantID:     nil,
		Email:        req.Email,
		PasswordHash: string(hash),
		Name:         req.Name,
		Role:         "admin",
	}
	if err := h.auth.CreateCredential(r.Context(), cred); err != nil {
		problem.InternalServerError(w, "failed to create admin credential")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"email": req.Email, "role": "admin"})
}

// Login validates email + password and returns the user profile and the tenant's API key.
// The API key is the bearer token for all subsequent /v1/* calls.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		problem.BadRequest(w, "request body is not valid JSON")
		return
	}

	cred, tenant, apiKey, err := h.auth.GetCredentialByEmail(r.Context(), req.Email)
	if err != nil {
		problem.InternalServerError(w, "credential lookup failed")
		return
	}
	if cred == nil {
		problem.Unauthorized(w, "invalid email or password")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(cred.PasswordHash), []byte(req.Password)); err != nil {
		problem.Unauthorized(w, "invalid email or password")
		return
	}

	resp := map[string]any{
		"id":    cred.ID,
		"name":  cred.Name,
		"email": cred.Email,
		"role":  cred.Role,
	}
	if tenant != nil {
		resp["tenant_id"] = tenant.ID
		resp["tenant_name"] = tenant.Name
	}
	if apiKey != nil && apiKey.RawKey != "" {
		resp["api_key"] = apiKey.RawKey
		resp["key_prefix"] = apiKey.KeyPrefix
	}

	writeJSON(w, http.StatusOK, resp)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func generateAPIKey() (raw, hash, prefix string, err error) {
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
