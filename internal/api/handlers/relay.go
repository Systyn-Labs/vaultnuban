package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/systynlabs/vaultnuban/internal/api/middleware"
	"github.com/systynlabs/vaultnuban/internal/api/problem"
	"github.com/systynlabs/vaultnuban/internal/domain"
	"github.com/systynlabs/vaultnuban/internal/relay"
	"github.com/systynlabs/vaultnuban/internal/store"
)

// RelayHandler manages tenant webhook endpoint registration (FR-11).
type RelayHandler struct {
	relayStore store.RelayStore
}

func NewRelayHandler(relayStore store.RelayStore) *RelayHandler {
	return &RelayHandler{relayStore: relayStore}
}

type createEndpointRequest struct {
	URL    string `json:"url"`
	Secret string `json:"secret"`
}

type endpointResponse struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	Active    bool   `json:"active"`
	CreatedAt string `json:"created_at"`
}

// CreateEndpoint handles POST /v1/webhook-endpoints.
func (h *RelayHandler) CreateEndpoint(w http.ResponseWriter, r *http.Request) {
	tenant := middleware.TenantFromContext(r.Context())

	var req createEndpointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		problem.BadRequest(w, "invalid JSON body")
		return
	}
	if req.URL == "" {
		problem.UnprocessableEntity(w, "missing-field", "url is required")
		return
	}
	if req.Secret == "" {
		problem.UnprocessableEntity(w, "missing-field", "secret is required (min 16 chars recommended)")
		return
	}

	ep := &domain.RelayEndpoint{
		TenantID:   tenant.ID,
		URL:        req.URL,
		SecretHash: relay.HashSecret(req.Secret),
		Active:     true,
	}
	if err := h.relayStore.CreateEndpoint(r.Context(), ep); err != nil {
		problem.InternalServerError(w, "failed to register webhook endpoint")
		return
	}

	writeJSON(w, http.StatusCreated, endpointResponse{
		ID:        ep.ID,
		URL:       ep.URL,
		Active:    ep.Active,
		CreatedAt: ep.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	})
}
