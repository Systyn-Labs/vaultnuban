package handlers

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/systynlabs/vaultnuban/internal/api/middleware"
	"github.com/systynlabs/vaultnuban/internal/api/problem"
	"github.com/systynlabs/vaultnuban/internal/domain"
	"github.com/systynlabs/vaultnuban/internal/service"
	"github.com/systynlabs/vaultnuban/internal/store"
)

// ── Request / response types ──────────────────────────────────────────────────

type vaResponse struct {
	ID          string `json:"id"`
	CustomerID  string `json:"customer_id"`
	NUBAN       string `json:"nuban"`
	BankName    string `json:"bank_name"`
	AccountName string `json:"account_name"`
	AccountRef  string `json:"account_ref"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type renameVARequest struct {
	AccountName string `json:"account_name"`
}

type patchVARequest struct {
	AccountName *string `json:"account_name,omitempty"`
	Status      string  `json:"status,omitempty"` // "SUSPENDED" | "ACTIVE"
}

// ── Handler ───────────────────────────────────────────────────────────────────

type VAHandler struct {
	svc   *service.ProvisioningService
	vaDB  store.VirtualAccountStore
}

func NewVAHandler(svc *service.ProvisioningService, vaDB store.VirtualAccountStore) *VAHandler {
	return &VAHandler{svc: svc, vaDB: vaDB}
}

// ListVAs handles GET /v1/virtual-accounts.
func (h *VAHandler) ListVAs(w http.ResponseWriter, r *http.Request) {
	tenant := middleware.TenantFromContext(r.Context())
	cursor := r.URL.Query().Get("cursor")

	vas, nextCursor, err := h.vaDB.ListVAs(r.Context(), tenant.ID, 50, cursor)
	if err != nil {
		serverErr(w, r, "ListVAs", err)
		return
	}

	type enrichedVAResponse struct {
		vaResponse
		CustomerDisplayName string `json:"customer_display_name,omitempty"`
	}

	data := make([]enrichedVAResponse, 0, len(vas))
	for _, va := range vas {
		data = append(data, enrichedVAResponse{
			vaResponse:          toVAResponse(va),
			CustomerDisplayName: va.CustomerDisplayName,
		})
	}

	resp := map[string]any{"data": data}
	if nextCursor != "" {
		resp["next_cursor"] = nextCursor
	}
	writeJSON(w, http.StatusOK, resp)
}

// ProvisionVA handles POST /v1/customers/{customerID}/virtual-account.
func (h *VAHandler) ProvisionVA(w http.ResponseWriter, r *http.Request) {
	customerID := chi.URLParam(r, "customerID")
	tenant := middleware.TenantFromContext(r.Context())
	actor := actorFromContext(r)

	va, created, err := h.svc.ProvisionVA(r.Context(), tenant.ID, customerID, actor)
	if err != nil {
		serverErr(w, r, "ProvisionVA", err)
		return
	}
	if va == nil {
		problem.NotFound(w, "customer not found")
		return
	}

	status := http.StatusCreated
	if !created {
		status = http.StatusOK // idempotent re-provision
	}
	writeJSON(w, status, toVAResponse(va))
}

// GetVA handles GET /v1/customers/{customerID}/virtual-account.
func (h *VAHandler) GetVA(w http.ResponseWriter, r *http.Request) {
	customerID := chi.URLParam(r, "customerID")
	tenant := middleware.TenantFromContext(r.Context())

	va, err := h.svc.GetVA(r.Context(), tenant.ID, customerID)
	if err != nil {
		serverErr(w, r, "GetVA", err)
		return
	}
	if va == nil {
		problem.NotFound(w, "virtual account not found")
		return
	}

	writeJSON(w, http.StatusOK, toVAResponse(va))
}

// PatchVA handles PATCH /v1/customers/{customerID}/virtual-account.
// Supports rename (account_name) and status change (status: SUSPENDED|ACTIVE).
func (h *VAHandler) PatchVA(w http.ResponseWriter, r *http.Request) {
	customerID := chi.URLParam(r, "customerID")
	tenant := middleware.TenantFromContext(r.Context())
	actor := actorFromContext(r)

	var req patchVARequest
	if err := decodeJSON(r, &req); err != nil {
		problem.BadRequest(w, "invalid JSON body")
		return
	}

	if req.AccountName != nil {
		va, err := h.svc.RenameVA(r.Context(), tenant.ID, customerID, *req.AccountName, actor)
		if err != nil {
			serverErr(w, r, "RenameVA", err)
			return
		}
		if va == nil {
			problem.NotFound(w, "virtual account not found")
			return
		}
		writeJSON(w, http.StatusOK, toVAResponse(va))
		return
	}

	switch req.Status {
	case "SUSPENDED":
		err := h.svc.SuspendVA(r.Context(), tenant.ID, customerID, actor)
		if err != nil {
			handleLifecycleError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "ACTIVE":
		err := h.svc.UnsuspendVA(r.Context(), tenant.ID, customerID, actor)
		if err != nil {
			handleLifecycleError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		problem.BadRequest(w, `body must contain account_name or status ("SUSPENDED"|"ACTIVE")`)
	}
}

// DeleteVA handles DELETE /v1/customers/{customerID}/virtual-account.
func (h *VAHandler) DeleteVA(w http.ResponseWriter, r *http.Request) {
	customerID := chi.URLParam(r, "customerID")
	tenant := middleware.TenantFromContext(r.Context())
	actor := actorFromContext(r)

	err := h.svc.CloseVA(r.Context(), tenant.ID, customerID, actor)
	if err != nil {
		handleLifecycleError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func toVAResponse(va *domain.VirtualAccount) vaResponse {
	return vaResponse{
		ID:          va.ID,
		CustomerID:  va.CustomerID,
		NUBAN:       va.NUBAN,
		BankName:    va.BankName,
		AccountName: va.AccountName,
		AccountRef:  va.NombaAccountRef,
		Status:      string(va.Status),
		CreatedAt:   va.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:   va.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

func handleLifecycleError(w http.ResponseWriter, r *http.Request, err error) {
	var se *service.StateError
	if errors.As(err, &se) {
		problem.Conflict(w, se.Error())
		return
	}
	serverErr(w, r, "LifecycleOp", err)
}
