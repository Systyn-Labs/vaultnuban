package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/systynlabs/vaultnuban/internal/api/middleware"
	"github.com/systynlabs/vaultnuban/internal/api/problem"
	"github.com/systynlabs/vaultnuban/internal/domain"
	"github.com/systynlabs/vaultnuban/internal/relay"
	"github.com/systynlabs/vaultnuban/internal/store"
)

// RelayHandler manages tenant webhook endpoint registration and delivery logs (FR-11).
type RelayHandler struct {
	relayStore store.RelayStore
	dispatcher *relay.Dispatcher
}

func NewRelayHandler(relayStore store.RelayStore, dispatcher *relay.Dispatcher) *RelayHandler {
	return &RelayHandler{relayStore: relayStore, dispatcher: dispatcher}
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
		serverErr(w, r, "CreateEndpoint", err)
		return
	}

	writeJSON(w, http.StatusCreated, endpointResponse{
		ID:        ep.ID,
		URL:       ep.URL,
		Active:    ep.Active,
		CreatedAt: ep.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	})
}

// ListDeliveries handles GET /v1/webhook-deliveries.
func (h *RelayHandler) ListDeliveries(w http.ResponseWriter, r *http.Request) {
	tenant := middleware.TenantFromContext(r.Context())
	cursor := r.URL.Query().Get("cursor")

	deliveries, nextCursor, err := h.relayStore.ListDeliveries(r.Context(), tenant.ID, 50, cursor)
	if err != nil {
		serverErr(w, r, "ListDeliveries", err)
		return
	}

	type deliveryResp struct {
		ID          string  `json:"id"`
		EndpointID  string  `json:"endpoint_id"`
		EventType   string  `json:"event_type"`
		Attempt     int     `json:"attempt"`
		Status      string  `json:"status"`
		StatusCode  *int    `json:"status_code,omitempty"`
		Error       *string `json:"error,omitempty"`
		NextRetryAt *string `json:"next_retry_at,omitempty"`
		DeliveredAt *string `json:"delivered_at,omitempty"`
		CreatedAt   string  `json:"created_at"`
	}

	data := make([]deliveryResp, 0, len(deliveries))
	for _, d := range deliveries {
		dr := deliveryResp{
			ID:         d.ID,
			EndpointID: d.EndpointID,
			EventType:  d.EventType,
			Attempt:    d.Attempt,
			Status:     d.Status,
			StatusCode: d.StatusCode,
			Error:      d.Error,
			CreatedAt:  d.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		}
		if d.NextRetryAt != nil {
			s := d.NextRetryAt.UTC().Format("2006-01-02T15:04:05Z")
			dr.NextRetryAt = &s
		}
		if d.DeliveredAt != nil {
			s := d.DeliveredAt.UTC().Format("2006-01-02T15:04:05Z")
			dr.DeliveredAt = &s
		}
		data = append(data, dr)
	}

	resp := map[string]any{"data": data}
	if nextCursor != "" {
		resp["next_cursor"] = nextCursor
	}
	writeJSON(w, http.StatusOK, resp)
}

// ReplayDelivery handles POST /v1/webhook-deliveries/{deliveryID}/replay.
func (h *RelayHandler) ReplayDelivery(w http.ResponseWriter, r *http.Request) {
	deliveryID := chi.URLParam(r, "deliveryID")
	tenant := middleware.TenantFromContext(r.Context())

	delivery, err := h.relayStore.GetDelivery(r.Context(), deliveryID)
	if err != nil {
		serverErr(w, r, "ReplayDelivery", err)
		return
	}
	if delivery == nil {
		problem.NotFound(w, "delivery not found")
		return
	}

	// Verify the delivery's endpoint belongs to this tenant.
	ep, err := h.relayStore.GetEndpoint(r.Context(), delivery.EndpointID)
	if err != nil {
		serverErr(w, r, "ReplayDelivery", err)
		return
	}
	if ep == nil || ep.TenantID != tenant.ID {
		problem.NotFound(w, "delivery not found")
		return
	}

	if err := h.dispatcher.Replay(r.Context(), delivery); err != nil {
		serverErr(w, r, "ReplayDelivery", err)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}
