package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/systynlabs/vaultnuban/internal/api/middleware"
	"github.com/systynlabs/vaultnuban/internal/api/problem"
	"github.com/systynlabs/vaultnuban/internal/service"
)

type SuspenseHandler struct {
	svc *service.SuspenseService
}

func NewSuspenseHandler(svc *service.SuspenseService) *SuspenseHandler {
	return &SuspenseHandler{svc: svc}
}

// ── Response types ────────────────────────────────────────────────────────────

type suspenseItemResponse struct {
	ID            string  `json:"id"`
	TransactionID string  `json:"transaction_id"`
	Reason        string  `json:"reason"`
	Status        string  `json:"status"`
	Notes         *string `json:"notes,omitempty"`
	ResolvedBy    *string `json:"resolved_by,omitempty"`
	CreatedAt     string  `json:"created_at"`
	AmountKobo    int64   `json:"amount_kobo"`
	NUBAN         string  `json:"nuban,omitempty"`
}

type listSuspenseResponse struct {
	Data       []suspenseItemResponse `json:"data"`
	NextCursor string                 `json:"next_cursor,omitempty"`
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// ListSuspense handles GET /v1/suspense.
func (h *SuspenseHandler) ListSuspense(w http.ResponseWriter, r *http.Request) {
	tenant := middleware.TenantFromContext(r.Context())

	limit := queryInt(r, "limit", 50)
	cursor := r.URL.Query().Get("cursor")

	items, nextCursor, err := h.svc.ListItems(r.Context(), tenant.ID, limit, cursor)
	if err != nil {
		serverErr(w, r, "ListSuspense", err)
		return
	}

	resp := listSuspenseResponse{
		Data:       make([]suspenseItemResponse, 0, len(items)),
		NextCursor: nextCursor,
	}
	for _, item := range items {
		resp.Data = append(resp.Data, suspenseItemResponse{
			ID:            item.ID,
			TransactionID: item.TransactionID,
			Reason:        string(item.Reason),
			Status:        item.Status,
			Notes:         item.Notes,
			ResolvedBy:    item.ResolvedBy,
			CreatedAt:     item.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			AmountKobo:    item.AmountKobo,
			NUBAN:         item.NUBAN,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// ResolveSuspense handles PATCH /v1/suspense/{itemID}.
func (h *SuspenseHandler) ResolveSuspense(w http.ResponseWriter, r *http.Request) {
	itemID := chi.URLParam(r, "itemID")
	tenant := middleware.TenantFromContext(r.Context())
	actor := actorFromContext(r)

	var body struct {
		Resolution       string `json:"resolution"`
		TargetCustomerID string `json:"target_customer_id"`
		Notes            string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		problem.BadRequest(w, "invalid JSON body")
		return
	}

	err := h.svc.Resolve(r.Context(), itemID, service.ResolveRequest{
		Resolution:       body.Resolution,
		TargetCustomerID: body.TargetCustomerID,
		Notes:            body.Notes,
		Actor:            actor,
		TenantID:         tenant.ID,
	})
	if err != nil {
		var nfe *service.NotFoundError
		var ve *service.ValidationError
		switch {
		case errors.As(err, &nfe):
			problem.NotFound(w, nfe.Error())
		case errors.As(err, &ve):
			problem.UnprocessableEntity(w, ve.Field, ve.Detail)
		default:
			serverErr(w, r, "ResolveSuspense", err)
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}
