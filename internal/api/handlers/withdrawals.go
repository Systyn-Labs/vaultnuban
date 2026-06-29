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
	"github.com/systynlabs/vaultnuban/internal/store"
)

type WithdrawalHandler struct {
	svc             *service.WithdrawalService
	withdrawalStore store.WithdrawalStore
}

func NewWithdrawalHandler(svc *service.WithdrawalService, ws store.WithdrawalStore) *WithdrawalHandler {
	return &WithdrawalHandler{svc: svc, withdrawalStore: ws}
}

// POST /v1/customers/{customerID}/withdrawals
func (h *WithdrawalHandler) Initiate(w http.ResponseWriter, r *http.Request) {
	customerID := chi.URLParam(r, "customerID")
	tenant := middleware.TenantFromContext(r.Context())

	var req struct {
		AmountKobo               int64  `json:"amount_kobo"`
		DestinationBankCode      string `json:"destination_bank_code"`
		DestinationAccountNumber string `json:"destination_account_number"`
		DestinationAccountName   string `json:"destination_account_name"`
		Narration                string `json:"narration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		problem.BadRequest(w, "invalid JSON body")
		return
	}

	withdrawal, err := h.svc.Initiate(r.Context(), service.WithdrawalRequest{
		CustomerID:               customerID,
		TenantID:                 tenant.ID,
		AmountKobo:               req.AmountKobo,
		DestinationBankCode:      req.DestinationBankCode,
		DestinationAccountNumber: req.DestinationAccountNumber,
		DestinationAccountName:   req.DestinationAccountName,
		Narration:                req.Narration,
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
			serverErr(w, r, "Initiate withdrawal", err)
		}
		return
	}

	writeJSON(w, http.StatusCreated, wdToResp(withdrawal))
}

// GET /v1/customers/{customerID}/withdrawals
func (h *WithdrawalHandler) List(w http.ResponseWriter, r *http.Request) {
	customerID := chi.URLParam(r, "customerID")
	limit := queryInt(r, "limit", 50)
	cursor := r.URL.Query().Get("cursor")

	items, next, err := h.withdrawalStore.ListWithdrawals(r.Context(), customerID, limit, cursor)
	if err != nil {
		serverErr(w, r, "ListWithdrawals", err)
		return
	}

	type resp struct {
		Data       []map[string]any `json:"data"`
		NextCursor string           `json:"next_cursor,omitempty"`
	}
	out := resp{Data: make([]map[string]any, 0, len(items)), NextCursor: next}
	for _, wd := range items {
		out.Data = append(out.Data, wdToResp(wd))
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /v1/payees/resolve?bank_code=X&account_number=Y
func (h *WithdrawalHandler) ResolvePayee(w http.ResponseWriter, r *http.Request) {
	bankCode := r.URL.Query().Get("bank_code")
	accountNumber := r.URL.Query().Get("account_number")
	if bankCode == "" || accountNumber == "" {
		problem.BadRequest(w, "bank_code and account_number are required")
		return
	}

	res, err := h.svc.ResolveAccount(r.Context(), bankCode, accountNumber)
	if err != nil {
		serverErr(w, r, "ResolvePayee", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"account_name":   res.AccountName,
		"account_number": res.AccountNumber,
		"bank_code":      res.BankCode,
	})
}

func wdToResp(w *domain.Withdrawal) map[string]any {
	m := map[string]any{
		"id":                         w.ID,
		"customer_id":                w.CustomerID,
		"amount_kobo":                w.AmountKobo,
		"destination_bank_code":      w.DestinationBankCode,
		"destination_account_number": w.DestinationAccountNumber,
		"destination_account_name":   w.DestinationAccountName,
		"narration":                  w.Narration,
		"status":                     w.Status,
		"created_at":                 w.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if w.ProviderTransactionID != nil {
		m["provider_transaction_id"] = *w.ProviderTransactionID
	}
	if w.FailureReason != nil {
		m["failure_reason"] = *w.FailureReason
	}
	return m
}
