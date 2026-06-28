package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	mw "github.com/systynlabs/vaultnuban/internal/api/middleware"
	"github.com/systynlabs/vaultnuban/internal/store"
)

type APIKeyHandler struct {
	tenantStore store.TenantStore
}

func NewAPIKeyHandler(ts store.TenantStore) *APIKeyHandler {
	return &APIKeyHandler{tenantStore: ts}
}

func (h *APIKeyHandler) ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	tenant := mw.TenantFromContext(r.Context())
	keys, err := h.tenantStore.ListAPIKeys(r.Context(), tenant.ID)
	if err != nil {
		serverErr(w, r, "ListAPIKeys", err)
		return
	}

	type keyResp struct {
		ID        string `json:"id"`
		Prefix    string `json:"prefix"`
		CreatedAt string `json:"created_at"`
	}
	type resp struct {
		Data []keyResp `json:"data"`
	}
	out := resp{Data: []keyResp{}}
	for _, k := range keys {
		out.Data = append(out.Data, keyResp{
			ID:        k.ID,
			Prefix:    k.KeyPrefix,
			CreatedAt: k.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (h *APIKeyHandler) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	tenant := mw.TenantFromContext(r.Context())

	raw, hash, prefix, err := generateAPIKey()
	if err != nil {
		serverErr(w, r, "CreateAPIKey", err)
		return
	}

	k, err := h.tenantStore.CreateAPIKey(r.Context(), tenant.ID, raw, hash, prefix)
	if err != nil {
		serverErr(w, r, "CreateAPIKey", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"id":         k.ID,
		"prefix":     k.KeyPrefix,
		"api_key":    raw,
		"created_at": k.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	})
}

func (h *APIKeyHandler) RevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	tenant := mw.TenantFromContext(r.Context())
	keyID := chi.URLParam(r, "keyID")

	if err := h.tenantStore.RevokeAPIKey(r.Context(), keyID, tenant.ID); err != nil {
		serverErr(w, r, "RevokeAPIKey", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

