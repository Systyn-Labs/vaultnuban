package nomba

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/systynlabs/vaultnuban/internal/provider"
)

// Adapter implements provider.Provider against the Nomba API.
type Adapter struct {
	client        *Client
	subAccountID  string
	webhookSecret string
}

func New(baseURL, clientID, clientSecret, accountID, subAccountID, webhookSecret string) *Adapter {
	return &Adapter{
		client:        NewClient(baseURL, clientID, clientSecret, accountID),
		subAccountID:  subAccountID,
		webhookSecret: webhookSecret,
	}
}

// ── Virtual account lifecycle ─────────────────────────────────────────────────

type createVARequest struct {
	AccountRef  string `json:"accountRef"`
	AccountName string `json:"accountName"`
	BVN         string `json:"bvn,omitempty"`
}

type createVAResponse struct {
	Code string `json:"code"`
	Data struct {
		BankAccountNumber string `json:"bankAccountNumber"`
		BankName          string `json:"bankName"`
		BankAccountName   string `json:"bankAccountName"`
		AccountHolderID   string `json:"accountHolderId"`
		AccountRef        string `json:"accountRef"`
	} `json:"data"`
}

func (a *Adapter) CreateVA(ctx context.Context, req provider.CreateVARequest) (*provider.VAResponse, error) {
	body, _ := json.Marshal(createVARequest{
		AccountRef:  req.AccountRef,
		AccountName: req.AccountName,
		BVN:         req.BVN,
	})

	path := "/v1/accounts/virtual"
	if a.subAccountID != "" {
		path = "/v1/accounts/virtual/" + a.subAccountID
	}
	resp, err := a.client.authDo(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, fmt.Errorf("nomba: create VA: %w", err)
	}
	defer resp.Body.Close()

	var out createVAResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("nomba: decode create VA: %w", err)
	}

	return &provider.VAResponse{
		AccountRef:      out.Data.AccountRef,
		NUBAN:           out.Data.BankAccountNumber,
		BankName:        out.Data.BankName,
		AccountName:     out.Data.BankAccountName,
		AccountHolderID: out.Data.AccountHolderID,
	}, nil
}

type updateVARequest struct {
	AccountName string `json:"accountName"`
}

func (a *Adapter) UpdateVA(ctx context.Context, identifier, newName string) error {
	body, _ := json.Marshal(updateVARequest{AccountName: newName})
	resp, err := a.client.authDo(ctx, http.MethodPut, "/v1/accounts/virtual/"+identifier, body)
	if err != nil {
		return fmt.Errorf("nomba: update VA: %w", err)
	}
	resp.Body.Close()
	return nil
}

func (a *Adapter) CloseVA(ctx context.Context, identifier string) error {
	resp, err := a.client.authDo(ctx, http.MethodDelete, "/v1/accounts/virtual/"+identifier, nil)
	if err != nil {
		return fmt.Errorf("nomba: close VA: %w", err)
	}
	resp.Body.Close()
	return nil
}

func (a *Adapter) SuspendVA(ctx context.Context, accountID string) error {
	resp, err := a.client.authDo(ctx, http.MethodPut, "/v1/accounts/suspend/"+accountID, nil)
	if err != nil {
		return fmt.Errorf("nomba: suspend VA: %w", err)
	}
	resp.Body.Close()
	return nil
}

// ── Virtual account listing ───────────────────────────────────────────────────

// listVAsResponse maps POST /v1/accounts/virtual/list. Accounts arrive under
// data.results; there is no status field — Nomba exposes an `expired` boolean.
type listVAsResponse struct {
	Code string `json:"code"`
	Data struct {
		Results []struct {
			AccountRef        string `json:"accountRef"`
			BankAccountNumber string `json:"bankAccountNumber"`
			BankName          string `json:"bankName"`
			BankAccountName   string `json:"bankAccountName"`
			Expired           bool   `json:"expired"`
			CreatedAt         string `json:"createdAt"`
		} `json:"results"`
		Cursor string `json:"cursor"`
	} `json:"data"`
}

func (a *Adapter) ListVAs(ctx context.Context, cursor string) (*provider.VAPage, error) {
	path := "/v1/accounts/virtual/list?limit=100"
	if cursor != "" {
		path += "&cursor=" + url.QueryEscape(cursor)
	}
	// Empty filter object: list everything.
	resp, err := a.client.authDo(ctx, http.MethodPost, path, []byte("{}"))
	if err != nil {
		return nil, fmt.Errorf("nomba: list VAs: %w", err)
	}
	defer resp.Body.Close()

	var out listVAsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("nomba: decode list VAs: %w", err)
	}

	page := &provider.VAPage{NextCursor: out.Data.Cursor}
	for _, r := range out.Data.Results {
		status := "active"
		if r.Expired {
			status = "expired"
		}
		page.VAs = append(page.VAs, provider.NombaVA{
			AccountRef:  r.AccountRef,
			NUBAN:       r.BankAccountNumber,
			BankName:    r.BankName,
			AccountName: r.BankAccountName,
			Status:      status,
			CreatedAt:   r.CreatedAt,
		})
	}
	return page, nil
}

// ── Transaction listing ───────────────────────────────────────────────────────

// nombaTransaction is the shape returned by GET /v1/transactions/accounts/…,
// GET /v1/transactions/virtual, and GET /v1/transactions/requery/{sessionId}
// (verified against the live API 2026-07-01; richer than the OpenAPI spec).
// It carries the VA matching fields the sweep needs (recipientAccountNumber,
// sessionId, sender info). Amounts are naira; these endpoints return them as
// JSON strings ("100.0") while other Nomba endpoints use numbers, so
// json.Number covers both.
type nombaTransaction struct {
	TransactionID string      `json:"id"`
	SessionID     string      `json:"sessionId"`
	Amount        json.Number `json:"amount"` // naira
	Type          string      `json:"type"`   // e.g. "vact_transfer"
	Status        string      `json:"status"`
	TimeCreated   string      `json:"timeCreated"`
	// VA matching fields
	RecipientAccountNumber string `json:"recipientAccountNumber"` // the VA NUBAN credited
	SenderName             string `json:"senderName"`
	BankName               string `json:"bankName"` // sender's bank
	Narration              string `json:"narration"`
	EntryType              string `json:"entryType"` // CREDIT | DEBIT
}

type listTransactionsResponse struct {
	Code string `json:"code"`
	Data struct {
		Transactions []nombaTransaction `json:"results"`
		Cursor       string             `json:"cursor"`
	} `json:"data"`
}

// nombaDateFormat is the timestamp layout Nomba uses without a timezone suffix.
const nombaDateFormat = "2006-01-02T15:04:05"

// nombaDateOnly is what /v1/transactions/virtual expects for dateFrom/dateTo.
const nombaDateOnly = "2006-01-02"

// ListTransactions pages the account transactions API. When a sub-account is
// configured it queries /v1/transactions/accounts/{subAccountId} — VAs created
// under a sub-account only surface there, not on /v1/transactions/virtual
// (verified against the live API). When VirtualAccount is set it uses the
// per-VA /v1/transactions/virtual endpoint instead.
func (a *Adapter) ListTransactions(ctx context.Context, req provider.ListTransactionsRequest) (*provider.TransactionPage, error) {
	q := url.Values{}
	if req.Cursor != "" {
		q.Set("cursor", req.Cursor)
	}
	if req.PageSize > 0 {
		q.Set("limit", fmt.Sprintf("%d", req.PageSize))
	}

	var path string
	if req.VirtualAccount != "" {
		path = "/v1/transactions/virtual"
		q.Set("virtual_account", req.VirtualAccount)
		q.Set("dateFrom", req.DateFrom.UTC().Format(nombaDateOnly))
		q.Set("dateTo", req.DateTo.UTC().Format(nombaDateOnly))
	} else {
		path = "/v1/transactions/accounts"
		if a.subAccountID != "" {
			path += "/" + a.subAccountID
		}
		q.Set("dateFrom", req.DateFrom.UTC().Format(nombaDateFormat))
		q.Set("dateTo", req.DateTo.UTC().Format(nombaDateFormat))
	}

	resp, err := a.client.authDo(ctx, http.MethodGet, path+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("nomba: list transactions: %w", err)
	}
	defer resp.Body.Close()

	var out listTransactionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("nomba: decode transactions: %w", err)
	}

	page := &provider.TransactionPage{NextCursor: out.Data.Cursor}
	for _, t := range out.Data.Transactions {
		// The accounts listing returns every transaction on the sub-account
		// (outbound transfers, fees, etc.). Only virtual account credits form
		// the ingest for this product; everything else is not ours to post.
		if t.Type != "vact_transfer" {
			continue
		}
		pt, err := convertTransaction(t)
		if err != nil {
			continue // skip malformed entries; sweep will retry on next window
		}
		page.Transactions = append(page.Transactions, pt)
	}
	return page, nil
}

type requeryResponse struct {
	Code string           `json:"code"`
	Data nombaTransaction `json:"data"`
}

func (a *Adapter) Requery(ctx context.Context, sessionID string) (*provider.ProviderTransaction, error) {
	resp, err := a.client.authDo(ctx, http.MethodGet, "/v1/transactions/requery/"+sessionID, nil)
	if err != nil {
		return nil, fmt.Errorf("nomba: requery: %w", err)
	}
	defer resp.Body.Close()

	var out requeryResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("nomba: decode requery: %w", err)
	}

	pt, err := convertTransaction(out.Data)
	if err != nil {
		return nil, err
	}
	return &pt, nil
}

// ── Outbound transfers ────────────────────────────────────────────────────────

type transferRequest struct {
	Amount        float64 `json:"amount"` // naira, not kobo
	AccountNumber string  `json:"accountNumber"`
	AccountName   string  `json:"accountName"`
	BankCode      string  `json:"bankCode"`
	MerchantTxRef string  `json:"merchantTxRef"` // idempotency key
	Narration     string  `json:"narration,omitempty"`
}

// transferResponse maps BankAccountTransferResult: the transfer identifier is
// data.id — Nomba does not return a transactionId or sessionId here.
type transferResponse struct {
	Code string `json:"code"`
	Data struct {
		ID     string `json:"id"`
		Amount string `json:"amount"`
		Status string `json:"status"` // SUCCESS | PENDING_BILLING
	} `json:"data"`
}

func (a *Adapter) Transfer(ctx context.Context, req provider.TransferRequest) (*provider.TransferResponse, error) {
	amountNaira := float64(req.AmountKobo) / 100.0
	body, _ := json.Marshal(transferRequest{
		Amount:        amountNaira,
		AccountNumber: req.DestinationAccountNumber,
		AccountName:   req.DestinationAccountName,
		BankCode:      req.DestinationBankCode,
		MerchantTxRef: req.Reference,
		Narration:     req.Narration,
	})

	path := "/v2/transfers/bank"
	if a.subAccountID != "" {
		path = "/v2/transfers/bank/" + a.subAccountID
	}
	resp, err := a.client.authDo(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, fmt.Errorf("nomba: transfer: %w", err)
	}
	defer resp.Body.Close()

	var out transferResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("nomba: decode transfer: %w", err)
	}

	return &provider.TransferResponse{
		TransactionID: out.Data.ID,
		Status:        out.Data.Status,
	}, nil
}

type lookupRequest struct {
	AccountNumber string `json:"accountNumber"`
	BankCode      string `json:"bankCode"`
}

// lookupResponse maps BankAccountLookupResult — only the account number and
// name come back; the bank code is echoed from the request.
type lookupResponse struct {
	Code string `json:"code"`
	Data struct {
		AccountName   string `json:"accountName"`
		AccountNumber string `json:"accountNumber"`
	} `json:"data"`
}

func (a *Adapter) ResolveAccount(ctx context.Context, bankCode, accountNumber string) (*provider.AccountResolution, error) {
	body, _ := json.Marshal(lookupRequest{
		AccountNumber: accountNumber,
		BankCode:      bankCode,
	})
	resp, err := a.client.authDo(ctx, http.MethodPost, "/v1/transfers/bank/lookup", body)
	if err != nil {
		return nil, fmt.Errorf("nomba: resolve account: %w", err)
	}
	defer resp.Body.Close()

	var out lookupResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("nomba: decode resolve account: %w", err)
	}

	return &provider.AccountResolution{
		AccountName:   out.Data.AccountName,
		AccountNumber: out.Data.AccountNumber,
		BankCode:      bankCode,
	}, nil
}

// ── Webhook types ─────────────────────────────────────────────────────────────

type nombaWebhookPayload struct {
	EventType string          `json:"event_type"`
	RequestID string          `json:"requestId"`
	Data      nombaWebhookData `json:"data"`
}

type nombaWebhookData struct {
	Merchant    nombaWebhookMerchant    `json:"merchant"`
	Transaction nombaWebhookTransaction `json:"transaction"`
	Customer    nombaWebhookCustomer    `json:"customer"`
}

type nombaWebhookMerchant struct {
	WalletID string `json:"walletId"`
	UserID   string `json:"userId"`
}

type nombaWebhookTransaction struct {
	TransactionID         string  `json:"transactionId"`
	SessionID             string  `json:"sessionId"`
	Type                  string  `json:"type"`
	Time                  string  `json:"time"`
	ResponseCode          string  `json:"responseCode"`
	AliasAccountNumber    string  `json:"aliasAccountNumber"`
	AliasAccountReference string  `json:"aliasAccountReference"`
	AliasAccountName      string  `json:"aliasAccountName"`
	TransactionAmount     float64 `json:"transactionAmount"`
	Narration             string  `json:"narration"`
}

type nombaWebhookCustomer struct {
	SenderName string `json:"senderName"`
	BankName   string `json:"bankName"`
}

// ── Webhook signature ─────────────────────────────────────────────────────────

// VerifyWebhookSignature implements FR-4.1.
// Nomba signs: HMAC-SHA256(secret, "eventType:requestId:userId:walletId:transactionId:type:txTime:responseCode:nombaTimestamp")
// encoded as base64. See https://developer.nomba.com/docs/api-basics/webhook
func (a *Adapter) VerifyWebhookSignature(_ context.Context, headers map[string]string, body []byte) error {
	sig := headers["nomba-signature"]
	ts := headers["nomba-timestamp"]
	if sig == "" || ts == "" {
		return errors.New("missing nomba-signature or nomba-timestamp header")
	}

	var p nombaWebhookPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return fmt.Errorf("nomba: parse webhook body for signature: %w", err)
	}

	responseCode := p.Data.Transaction.ResponseCode
	if responseCode == "null" {
		responseCode = ""
	}

	hashingPayload := strings.Join([]string{
		p.EventType,
		p.RequestID,
		p.Data.Merchant.UserID,
		p.Data.Merchant.WalletID,
		p.Data.Transaction.TransactionID,
		p.Data.Transaction.Type,
		p.Data.Transaction.Time,
		responseCode,
		ts,
	}, ":")

	mac := hmac.New(sha256.New, []byte(a.webhookSecret))
	mac.Write([]byte(hashingPayload))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return errors.New("webhook signature mismatch")
	}
	return nil
}

// ── Webhook parsing ───────────────────────────────────────────────────────────

func (a *Adapter) ParseWebhook(_ context.Context, body []byte) (*provider.WebhookPayload, error) {
	var p nombaWebhookPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("nomba: parse webhook: %w", err)
	}

	eventType := normaliseEventType(p.EventType)

	wt := p.Data.Transaction
	occurredAt, _ := time.Parse(time.RFC3339, wt.Time)

	pt := provider.ProviderTransaction{
		TransactionID: wt.TransactionID,
		SessionID:     wt.SessionID,
		AccountNumber: wt.AliasAccountNumber,
		AccountRef:    wt.AliasAccountReference,
		AmountKobo:    int64(wt.TransactionAmount * 100),
		Type:          wt.Type,
		SenderName:    p.Data.Customer.SenderName,
		SenderBank:    p.Data.Customer.BankName,
		Narration:     wt.Narration,
		OccurredAt:    occurredAt,
		Raw:           body,
	}

	return &provider.WebhookPayload{
		EventType:   eventType,
		Transaction: pt,
		Raw:         body,
	}, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// parseNombaTime handles the timestamp variants Nomba emits (RFC3339 with or
// without a timezone suffix).
func parseNombaTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err == nil {
		return t, nil
	}
	return time.Parse(nombaDateFormat, s)
}

// convertTransaction maps a nombaTransaction (virtual account listing or
// requery) to a ProviderTransaction.
func convertTransaction(t nombaTransaction) (provider.ProviderTransaction, error) {
	occurredAt, err := parseNombaTime(t.TimeCreated)
	if err != nil {
		return provider.ProviderTransaction{}, fmt.Errorf("nomba: parse time %q: %w", t.TimeCreated, err)
	}
	amountKobo, err := nairaToKobo(t.Amount)
	if err != nil {
		return provider.ProviderTransaction{}, fmt.Errorf("nomba: parse amount %q: %w", t.Amount, err)
	}
	raw, _ := json.Marshal(t)
	return provider.ProviderTransaction{
		TransactionID: t.TransactionID,
		SessionID:     t.SessionID,
		AccountNumber: t.RecipientAccountNumber,
		AmountKobo:    amountKobo,
		Type:          t.Type,
		Status:        t.Status,
		SenderName:    t.SenderName,
		SenderBank:    t.BankName,
		Narration:     t.Narration,
		OccurredAt:    occurredAt,
		Raw:           raw,
	}, nil
}

// nairaToKobo converts a naira amount (string or number in the JSON — Nomba is
// inconsistent across endpoints) to kobo, rounding to the nearest kobo to avoid
// float truncation errors (e.g. 15.55 naira → 1555 kobo, not 1554).
func nairaToKobo(n json.Number) (int64, error) {
	if n == "" {
		return 0, nil
	}
	f, err := n.Float64()
	if err != nil {
		return 0, err
	}
	return int64(f*100 + 0.5), nil
}

func normaliseEventType(raw string) string {
	switch strings.ToLower(raw) {
	case "payment_success", "successful_transaction":
		return "payment_success"
	case "payment_reversal", "reversal":
		return "payment_reversal"
	case "payment_failed", "failed_transaction":
		return "payment_failed"
	default:
		return raw
	}
}
