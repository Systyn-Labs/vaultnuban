package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/systynlabs/vaultnuban/internal/api/middleware"
	"github.com/systynlabs/vaultnuban/internal/api/problem"
	"github.com/systynlabs/vaultnuban/internal/domain"
	"github.com/systynlabs/vaultnuban/internal/store"
)

// TransactionHandler serves listing and statement endpoints.
type TransactionHandler struct {
	txns     store.TransactionStore
	accounts store.VirtualAccountStore
	customers store.CustomerStore
}

func NewTransactionHandler(
	txns store.TransactionStore,
	accounts store.VirtualAccountStore,
	customers store.CustomerStore,
) *TransactionHandler {
	return &TransactionHandler{txns: txns, accounts: accounts, customers: customers}
}

// ── Response types ────────────────────────────────────────────────────────────

type txnResponse struct {
	ID         string  `json:"id"`
	AmountKobo int64   `json:"amount_kobo"`
	AmountNGN  string  `json:"amount_ngn"`
	Direction  string  `json:"direction"`
	Source     string  `json:"source"`
	Status     string  `json:"status"`
	SenderName *string `json:"sender_name,omitempty"`
	SenderBank *string `json:"sender_bank,omitempty"`
	Narration  *string `json:"narration,omitempty"`
	OccurredAt string  `json:"occurred_at"`
}

type listTxnsResponse struct {
	Data       []txnResponse `json:"data"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

type statementEntryResponse struct {
	OccurredAt     string `json:"occurred_at"`
	Description    string `json:"description"`
	DebitKobo      int64  `json:"debit_kobo"`
	CreditKobo     int64  `json:"credit_kobo"`
	DebitNGN       string `json:"debit_ngn"`
	CreditNGN      string `json:"credit_ngn"`
	RunningBalance int64  `json:"running_balance_kobo"`
	RunningNGN     string `json:"running_balance_ngn"`
}

type statementResponse struct {
	CustomerID         string                   `json:"customer_id"`
	From               string                   `json:"from"`
	To                 string                   `json:"to"`
	OpeningBalanceKobo int64                    `json:"opening_balance_kobo"`
	OpeningBalanceNGN  string                   `json:"opening_balance_ngn"`
	ClosingBalanceKobo int64                    `json:"closing_balance_kobo"`
	ClosingBalanceNGN  string                   `json:"closing_balance_ngn"`
	Entries            []statementEntryResponse `json:"entries"`
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// ListTransactions handles GET /v1/customers/{customerID}/transactions.
func (h *TransactionHandler) ListTransactions(w http.ResponseWriter, r *http.Request) {
	customerID := chi.URLParam(r, "customerID")
	tenant := middleware.TenantFromContext(r.Context())

	// Validate customer belongs to tenant.
	customer, err := h.customers.GetCustomer(r.Context(), tenant.ID, customerID)
	if err != nil {
		problem.InternalServerError(w, "failed to look up customer")
		return
	}
	if customer == nil {
		problem.NotFound(w, "customer not found")
		return
	}

	// Get any VA for this customer (active or closed) for the VA ID.
	va, err := h.accounts.GetActiveVA(r.Context(), customerID)
	if err != nil {
		problem.InternalServerError(w, "failed to look up virtual account")
		return
	}
	if va == nil {
		// Customer exists but has no VA yet — return empty list.
		writeJSON(w, http.StatusOK, listTxnsResponse{Data: []txnResponse{}})
		return
	}

	limit := queryInt(r, "limit", 50)
	cursor := r.URL.Query().Get("cursor")

	txns, nextCursor, err := h.txns.ListTransactions(r.Context(), va.ID, limit, cursor)
	if err != nil {
		problem.InternalServerError(w, "failed to list transactions")
		return
	}

	resp := listTxnsResponse{
		Data:       make([]txnResponse, 0, len(txns)),
		NextCursor: nextCursor,
	}
	for _, tx := range txns {
		resp.Data = append(resp.Data, toTxnResponse(tx))
	}
	writeJSON(w, http.StatusOK, resp)
}

// GetStatement handles GET /v1/customers/{customerID}/statement?from=&to=.
func (h *TransactionHandler) GetStatement(w http.ResponseWriter, r *http.Request) {
	customerID := chi.URLParam(r, "customerID")
	tenant := middleware.TenantFromContext(r.Context())

	customer, err := h.customers.GetCustomer(r.Context(), tenant.ID, customerID)
	if err != nil {
		problem.InternalServerError(w, "failed to look up customer")
		return
	}
	if customer == nil {
		problem.NotFound(w, "customer not found")
		return
	}

	from, to, ok := parseDateRange(r)
	if !ok {
		problem.BadRequest(w, "from and to must be RFC3339 timestamps (e.g. 2026-01-01T00:00:00Z)")
		return
	}

	walletAccount := domain.CustomerWalletAccount(customerID)
	stmt, err := h.txns.GetStatement(r.Context(), walletAccount, from, to)
	if err != nil {
		problem.InternalServerError(w, "failed to generate statement")
		return
	}

	entries := make([]statementEntryResponse, 0, len(stmt.Entries))
	for _, e := range stmt.Entries {
		entries = append(entries, statementEntryResponse{
			OccurredAt:     e.OccurredAt.Format(time.RFC3339),
			Description:    e.Description,
			DebitKobo:      e.DebitKobo,
			CreditKobo:     e.CreditKobo,
			DebitNGN:       koboToNGN(e.DebitKobo),
			CreditNGN:      koboToNGN(e.CreditKobo),
			RunningBalance: e.RunningBalance,
			RunningNGN:     koboToNGN(e.RunningBalance),
		})
	}

	writeJSON(w, http.StatusOK, statementResponse{
		CustomerID:         customerID,
		From:               from.Format(time.RFC3339),
		To:                 to.Format(time.RFC3339),
		OpeningBalanceKobo: stmt.OpeningBalanceKobo,
		OpeningBalanceNGN:  koboToNGN(stmt.OpeningBalanceKobo),
		ClosingBalanceKobo: stmt.ClosingBalanceKobo,
		ClosingBalanceNGN:  koboToNGN(stmt.ClosingBalanceKobo),
		Entries:            entries,
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func toTxnResponse(tx *domain.Transaction) txnResponse {
	return txnResponse{
		ID:         tx.ID,
		AmountKobo: tx.AmountKobo,
		AmountNGN:  koboToNGN(tx.AmountKobo),
		Direction:  tx.Direction,
		Source:     tx.Source,
		Status:     tx.Status,
		SenderName: tx.SenderName,
		SenderBank: tx.SenderBank,
		Narration:  tx.Narration,
		OccurredAt: tx.OccurredAt.Format(time.RFC3339),
	}
}

func koboToNGN(kobo int64) string {
	if kobo < 0 {
		return "-" + koboToNGN(-kobo)
	}
	return strconv.FormatInt(kobo/100, 10) + "." + zeroPad(kobo%100)
}

func zeroPad(v int64) string {
	if v < 10 {
		return "0" + strconv.FormatInt(v, 10)
	}
	return strconv.FormatInt(v, 10)
}

func queryInt(r *http.Request, key string, def int) int {
	s := r.URL.Query().Get(key)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil || v <= 0 {
		return def
	}
	return v
}

func parseDateRange(r *http.Request) (from, to time.Time, ok bool) {
	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
	if fromStr == "" || toStr == "" {
		return time.Time{}, time.Time{}, false
	}
	var err error
	from, err = time.Parse(time.RFC3339, fromStr)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	to, err = time.Parse(time.RFC3339, toStr)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	if to.Before(from) {
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}
