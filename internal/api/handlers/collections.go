package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/systynlabs/vaultnuban/internal/api/middleware"
	"github.com/systynlabs/vaultnuban/internal/api/problem"
	"github.com/systynlabs/vaultnuban/internal/domain"
	"github.com/systynlabs/vaultnuban/internal/service"
)

type CollectionHandler struct {
	svc *service.CollectionService
}

func NewCollectionHandler(svc *service.CollectionService) *CollectionHandler {
	return &CollectionHandler{svc: svc}
}

// POST /v1/customers/{customerID}/collections
func (h *CollectionHandler) Create(w http.ResponseWriter, r *http.Request) {
	customerID := chi.URLParam(r, "customerID")
	tenant := middleware.TenantFromContext(r.Context())

	var req struct {
		ExpectedAmountKobo *int64 `json:"expected_amount_kobo"`
		Reference          string `json:"reference"`
		Description        string `json:"description"`
		ExpiresInSeconds   int    `json:"expires_in_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		problem.BadRequest(w, "invalid JSON body")
		return
	}

	col, err := h.svc.Create(r.Context(), service.CreateCollectionRequest{
		CustomerID:         customerID,
		TenantID:           tenant.ID,
		ExpectedAmountKobo: req.ExpectedAmountKobo,
		Reference:          req.Reference,
		Description:        req.Description,
		ExpiresInSeconds:   req.ExpiresInSeconds,
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
			serverErr(w, r, "Create collection", err)
		}
		return
	}

	writeJSON(w, http.StatusCreated, colToResp(col))
}

// GET /v1/customers/{customerID}/collections
func (h *CollectionHandler) List(w http.ResponseWriter, r *http.Request) {
	customerID := chi.URLParam(r, "customerID")
	limit := queryInt(r, "limit", 50)
	cursor := r.URL.Query().Get("cursor")

	items, next, err := h.svc.List(r.Context(), customerID, limit, cursor)
	if err != nil {
		serverErr(w, r, "ListCollections", err)
		return
	}

	type resp struct {
		Data       []map[string]any `json:"data"`
		NextCursor string           `json:"next_cursor,omitempty"`
	}
	out := resp{Data: make([]map[string]any, 0, len(items)), NextCursor: next}
	for _, c := range items {
		out.Data = append(out.Data, colToResp(c))
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /v1/customers/{customerID}/collections/{collectionID}
func (h *CollectionHandler) Get(w http.ResponseWriter, r *http.Request) {
	collectionID := chi.URLParam(r, "collectionID")

	col, err := h.svc.Get(r.Context(), collectionID)
	if err != nil {
		var nfe *service.NotFoundError
		if errors.As(err, &nfe) {
			problem.NotFound(w, nfe.Error())
			return
		}
		serverErr(w, r, "GetCollection", err)
		return
	}

	writeJSON(w, http.StatusOK, colToResp(col))
}

// DELETE /v1/customers/{customerID}/collections/{collectionID}
func (h *CollectionHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	collectionID := chi.URLParam(r, "collectionID")
	tenant := middleware.TenantFromContext(r.Context())

	if err := h.svc.Cancel(r.Context(), collectionID, tenant.ID); err != nil {
		var nfe *service.NotFoundError
		var ve *service.ValidationError
		switch {
		case errors.As(err, &nfe):
			problem.NotFound(w, nfe.Error())
		case errors.As(err, &ve):
			problem.UnprocessableEntity(w, ve.Field, ve.Detail)
		default:
			serverErr(w, r, "CancelCollection", err)
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func colToResp(c *domain.Collection) map[string]any {
	m := map[string]any{
		"id":          c.ID,
		"customer_id": c.CustomerID,
		"reference":   c.Reference,
		"description": c.Description,
		"status":      c.Status,
		"nuban":       c.NUBAN,
		"bank_name":   c.BankName,
		"created_at":  c.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if c.ExpectedAmountKobo != nil {
		m["expected_amount_kobo"] = *c.ExpectedAmountKobo
	}
	if c.ExpiresAt != nil {
		m["expires_at"] = c.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	if c.FulfilledByTxnID != nil {
		m["fulfilled_by_txn_id"] = *c.FulfilledByTxnID
	}
	if c.FulfilledAt != nil {
		m["fulfilled_at"] = c.FulfilledAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	return m
}
