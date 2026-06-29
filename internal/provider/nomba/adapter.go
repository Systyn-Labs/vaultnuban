package nomba

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
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
	Currency    string `json:"currency"`
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
		Currency:    "NGN",
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

type listVAsResponse struct {
	Code string `json:"code"`
	Data struct {
		Accounts []struct {
			AccountRef      string `json:"accountRef"`
			BankAccountNumber string `json:"bankAccountNumber"`
			BankName        string `json:"bankName"`
			BankAccountName string `json:"bankAccountName"`
			Status          string `json:"status"`
			CreatedAt       string `json:"createdAt"`
		} `json:"accounts"`
		Cursor string `json:"cursor"`
	} `json:"data"`
}

func (a *Adapter) ListVAs(ctx context.Context, cursor string) (*provider.VAPage, error) {
	path := "/v1/accounts/virtual?limit=100"
	if cursor != "" {
		path += "&cursor=" + url.QueryEscape(cursor)
	}
	resp, err := a.client.authDo(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("nomba: list VAs: %w", err)
	}
	defer resp.Body.Close()

	var out listVAsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("nomba: decode list VAs: %w", err)
	}

	page := &provider.VAPage{NextCursor: out.Data.Cursor}
	for _, a := range out.Data.Accounts {
		page.VAs = append(page.VAs, provider.NombaVA{
			AccountRef:  a.AccountRef,
			NUBAN:       a.BankAccountNumber,
			BankName:    a.BankName,
			AccountName: a.BankAccountName,
			Status:      a.Status,
			CreatedAt:   a.CreatedAt,
		})
	}
	return page, nil
}

// ── Transaction listing ───────────────────────────────────────────────────────

// nombaListTransaction is the shape returned by GET /v1/transactions/accounts.
// Most matching fields are absent; sessionId is included so the sweep can requery
// each transaction to retrieve the full payload (accountRef, accountNumber, etc.).
type nombaListTransaction struct {
	TransactionID string `json:"id"`
	SessionID     string `json:"sessionId"` // present when available; used for requery
	Amount        string `json:"amount"`    // Nomba returns naira as a string
	Type          string `json:"type"`
	Status        string `json:"status"`
	CreatedAt     string `json:"timeCreated"`
}

// nombaTransaction is the shape delivered inside webhook payloads and requery responses.
// These carry the full set of fields needed for VA matching.
type nombaTransaction struct {
	TransactionID string `json:"transactionId"`
	SessionID     string `json:"sessionId"`
	AccountNumber string `json:"accountNumber"`
	AccountRef    string `json:"accountRef"`
	Amount        string `json:"amount"` // Nomba returns naira as a string
	Type          string `json:"type"`
	Status        string `json:"status"`
	SenderName    string `json:"senderName"`
	SenderBank    string `json:"senderBankName"`
	Narration     string `json:"narration"`
	CreatedAt     string `json:"createdAt"`
}

type listTransactionsResponse struct {
	Code string `json:"code"`
	Data struct {
		Transactions []nombaListTransaction `json:"results"` // Nomba uses "results" not "transactions"
		Cursor       string                 `json:"cursor"`
	} `json:"data"`
}

// nombaDateFormat is what the Nomba transaction list API expects — UTC with no timezone suffix.
const nombaDateFormat = "2006-01-02T15:04:05"

type filterTransactionRequest struct {
	Type string `json:"type"`
}

func (a *Adapter) ListTransactions(ctx context.Context, req provider.ListTransactionsRequest) (*provider.TransactionPage, error) {
	q := url.Values{}
	q.Set("dateFrom", req.DateFrom.UTC().Format(nombaDateFormat))
	q.Set("dateTo", req.DateTo.UTC().Format(nombaDateFormat))
	if req.Cursor != "" {
		q.Set("cursor", req.Cursor)
	}
	if req.PageSize > 0 {
		q.Set("limit", fmt.Sprintf("%d", req.PageSize))
	}

	// When a sub-account is configured, use the POST filter endpoint with
	// type=transfer so Nomba filters server-side. Without it, the sub-account
	// returns all activity (POS, purchases, etc.) across hundreds of pages.
	var (
		method string
		path   string
		body   []byte
	)
	if a.subAccountID != "" {
		method = http.MethodPost
		path = "/v1/transactions/accounts/" + a.subAccountID
		body, _ = json.Marshal(filterTransactionRequest{Type: "transfer"})
	} else {
		method = http.MethodGet
		path = "/v1/transactions/accounts"
	}

	resp, err := a.client.authDo(ctx, method, path+"?"+q.Encode(), body)
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
		pt, err := convertListTransaction(t)
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

type transferResponse struct {
	Code string `json:"code"`
	Data struct {
		TransactionID string `json:"transactionId"`
		SessionID     string `json:"sessionId"`
		Amount        string `json:"amount"`
		Status        string `json:"status"`
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
		TransactionID: out.Data.TransactionID,
		SessionID:     out.Data.SessionID,
		Status:        out.Data.Status,
	}, nil
}

type resolveAccountResponse struct {
	Code string `json:"code"`
	Data struct {
		AccountName   string `json:"accountName"`
		AccountNumber string `json:"accountNumber"`
		BankCode      string `json:"bankCode"`
	} `json:"data"`
}

func (a *Adapter) ResolveAccount(ctx context.Context, bankCode, accountNumber string) (*provider.AccountResolution, error) {
	q := url.Values{}
	q.Set("bankCode", bankCode)
	q.Set("accountNumber", accountNumber)
	resp, err := a.client.authDo(ctx, http.MethodGet, "/v1/resolve/account?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("nomba: resolve account: %w", err)
	}
	defer resp.Body.Close()

	var out resolveAccountResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("nomba: decode resolve account: %w", err)
	}

	return &provider.AccountResolution{
		AccountName:   out.Data.AccountName,
		AccountNumber: out.Data.AccountNumber,
		BankCode:      out.Data.BankCode,
	}, nil
}

// ── Webhook signature ─────────────────────────────────────────────────────────

// VerifyWebhookSignature implements FR-4.1.
// Nomba signs with HMAC-SHA256 over a colon-joined field string that includes
// the nomba-timestamp header. Comparison is constant-time.
func (a *Adapter) VerifyWebhookSignature(_ context.Context, headers map[string]string, body []byte) error {
	sig := headers["nomba-signature"]
	ts := headers["nomba-timestamp"]
	if sig == "" || ts == "" {
		return errors.New("missing nomba-signature or nomba-timestamp header")
	}

	// Nomba's signed string: timestamp + ":" + raw body
	// (exact format confirmed from Nomba webhook docs; adjust if sandbox differs)
	signed := ts + ":" + string(body)
	mac := hmac.New(sha256.New, []byte(a.webhookSecret))
	mac.Write([]byte(signed))
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return errors.New("webhook signature mismatch")
	}
	return nil
}

// ── Webhook parsing ───────────────────────────────────────────────────────────

type webhookPayload struct {
	Event string           `json:"event"`
	Data  nombaTransaction `json:"data"`
}

func (a *Adapter) ParseWebhook(_ context.Context, body []byte) (*provider.WebhookPayload, error) {
	var p webhookPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("nomba: parse webhook: %w", err)
	}

	// Normalise event name to our canonical types
	eventType := normaliseEventType(p.Event)

	pt, err := convertTransaction(p.Data)
	if err != nil && p.Data.TransactionID != "" {
		return nil, err
	}
	pt.Raw = body

	return &provider.WebhookPayload{
		EventType:   eventType,
		Transaction: pt,
		Raw:         body,
	}, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// convertListTransaction maps a nombaListTransaction (from the polling API) to a
// ProviderTransaction. Matching fields (accountRef, accountNumber) are absent from
// this API, but sessionId is forwarded so the sweep can requery each transaction
// to retrieve the full payload before attempting to match.
func convertListTransaction(t nombaListTransaction) (provider.ProviderTransaction, error) {
	var occurredAt time.Time
	if t.CreatedAt != "" {
		var err error
		occurredAt, err = time.Parse(nombaDateFormat, t.CreatedAt)
		if err != nil {
			occurredAt, err = time.Parse(time.RFC3339, t.CreatedAt)
			if err != nil {
				return provider.ProviderTransaction{}, fmt.Errorf("nomba: parse time %q: %w", t.CreatedAt, err)
			}
		}
	}
	amountKobo, err := parseNairaString(t.Amount)
	if err != nil {
		return provider.ProviderTransaction{}, fmt.Errorf("nomba: parse amount %q: %w", t.Amount, err)
	}
	raw, _ := json.Marshal(t)
	return provider.ProviderTransaction{
		TransactionID: t.TransactionID,
		SessionID:     t.SessionID, // forwarded for requery
		AmountKobo:    amountKobo,
		Type:          t.Type,
		Status:        t.Status,
		OccurredAt:    occurredAt,
		Raw:           raw,
	}, nil
}

func convertTransaction(t nombaTransaction) (provider.ProviderTransaction, error) {
	var occurredAt time.Time
	if t.CreatedAt != "" {
		var err error
		occurredAt, err = time.Parse(time.RFC3339, t.CreatedAt)
		if err != nil {
			// Try without timezone suffix
			occurredAt, err = time.Parse("2006-01-02T15:04:05", t.CreatedAt)
			if err != nil {
				return provider.ProviderTransaction{}, fmt.Errorf("nomba: parse time %q: %w", t.CreatedAt, err)
			}
		}
	}

	amountKobo, err := parseNairaString(t.Amount)
	if err != nil {
		return provider.ProviderTransaction{}, fmt.Errorf("nomba: parse amount %q: %w", t.Amount, err)
	}
	raw, _ := json.Marshal(t)
	return provider.ProviderTransaction{
		TransactionID: t.TransactionID,
		SessionID:     t.SessionID,
		AccountNumber: t.AccountNumber,
		AccountRef:    t.AccountRef,
		AmountKobo:    amountKobo,
		Type:          t.Type,
		Status:        t.Status,
		SenderName:    t.SenderName,
		SenderBank:    t.SenderBank,
		Narration:     t.Narration,
		OccurredAt:    occurredAt,
		Raw:           raw,
	}, nil
}

// parseNairaString converts a naira amount string (e.g. "1500.00") to kobo (int64).
// Handles both integer and decimal representations.
func parseNairaString(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	return int64(f * 100), nil
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
