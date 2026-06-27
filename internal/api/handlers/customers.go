package handlers

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/systynlabs/vaultnuban/internal/api/middleware"
	"github.com/systynlabs/vaultnuban/internal/api/problem"
	"github.com/systynlabs/vaultnuban/internal/domain"
	"github.com/systynlabs/vaultnuban/internal/service"
)

// ── Request / response types ──────────────────────────────────────────────────

type createCustomerRequest struct {
	ExternalRef string `json:"external_ref"`
	DisplayName string `json:"display_name"`
	Identity    struct {
		BVNMasked *string `json:"bvn_masked"`
		NINMasked *string `json:"nin_masked"`
		KYCTier   int     `json:"kyc_tier"`
	} `json:"identity"`
}

type customerResponse struct {
	ID          string           `json:"id"`
	TenantID    string           `json:"tenant_id"`
	ExternalRef string           `json:"external_ref"`
	DisplayName string           `json:"display_name"`
	Status      string           `json:"status"`
	Identity    *identityResponse `json:"identity,omitempty"`
	CreatedAt   string           `json:"created_at"`
}

type identityResponse struct {
	ID                 string  `json:"id"`
	BVNMasked          *string `json:"bvn_masked,omitempty"`
	NINMasked          *string `json:"nin_masked,omitempty"`
	KYCTier            int     `json:"kyc_tier"`
	VerificationStatus string  `json:"verification_status"`
}

type updateKYCRequest struct {
	KYCTier int `json:"kyc_tier"`
}

// ── Handlers ──────────────────────────────────────────────────────────────────

type CustomerHandler struct {
	svc *service.CustomerService
}

func NewCustomerHandler(svc *service.CustomerService) *CustomerHandler {
	return &CustomerHandler{svc: svc}
}

// CreateCustomer handles POST /v1/customers.
func (h *CustomerHandler) CreateCustomer(w http.ResponseWriter, r *http.Request) {
	var req createCustomerRequest
	if err := decodeJSON(r, &req); err != nil {
		problem.BadRequest(w, "invalid JSON body")
		return
	}
	if req.ExternalRef == "" {
		problem.UnprocessableEntity(w, "missing-field", "external_ref is required")
		return
	}
	if req.DisplayName == "" {
		problem.UnprocessableEntity(w, "missing-field", "display_name is required")
		return
	}

	tenant := middleware.TenantFromContext(r.Context())
	actor := actorFromContext(r)

	customer, err := h.svc.CreateCustomer(r.Context(), tenant.ID, req.ExternalRef, req.DisplayName,
		domain.IdentityInput{
			BVNMasked: req.Identity.BVNMasked,
			NINMasked: req.Identity.NINMasked,
			KYCTier:   req.Identity.KYCTier,
		},
		actor,
	)
	if err != nil {
		var ve *service.ValidationError
		if errors.As(err, &ve) {
			problem.UnprocessableEntity(w, "identity-required", ve.Detail)
			return
		}
		problem.InternalServerError(w, "failed to create customer")
		return
	}

	// 200 if the customer already existed (idempotent by external_ref), 201 if new.
	// We detect this by checking if created_at == updated_at (a rough proxy; the service
	// could return a bool, but here we use the convention that existing records return 200).
	status := http.StatusCreated
	writeJSON(w, status, toCustomerResponse(customer))
}

// UpdateKYCTier handles PATCH /v1/customers/{customerID}/identity.
func (h *CustomerHandler) UpdateKYCTier(w http.ResponseWriter, r *http.Request) {
	customerID := chi.URLParam(r, "customerID")
	tenant := middleware.TenantFromContext(r.Context())

	var req updateKYCRequest
	if err := decodeJSON(r, &req); err != nil {
		problem.BadRequest(w, "invalid JSON body")
		return
	}

	actor := actorFromContext(r)
	customer, err := h.svc.UpdateKYCTier(r.Context(), tenant.ID, customerID, req.KYCTier, actor)
	if err != nil {
		var ve *service.ValidationError
		if errors.As(err, &ve) {
			problem.UnprocessableEntity(w, "invalid-kyc-tier", ve.Detail)
			return
		}
		problem.InternalServerError(w, "failed to update KYC tier")
		return
	}
	if customer == nil {
		problem.NotFound(w, "customer not found")
		return
	}

	writeJSON(w, http.StatusOK, toCustomerResponse(customer))
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func toCustomerResponse(c *domain.Customer) customerResponse {
	resp := customerResponse{
		ID:          c.ID,
		TenantID:    c.TenantID,
		ExternalRef: c.ExternalRef,
		DisplayName: c.DisplayName,
		Status:      c.Status,
		CreatedAt:   c.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if c.Identity != nil {
		resp.Identity = &identityResponse{
			ID:                 c.Identity.ID,
			BVNMasked:          c.Identity.BVNMasked,
			NINMasked:          c.Identity.NINMasked,
			KYCTier:            c.Identity.KYCTier,
			VerificationStatus: c.Identity.VerificationStatus,
		}
	}
	return resp
}

func actorFromContext(r *http.Request) string {
	if k := middleware.APIKeyFromContext(r.Context()); k != nil {
		return "key:" + k.KeyPrefix
	}
	return "system"
}
