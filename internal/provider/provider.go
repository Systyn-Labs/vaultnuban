// Package provider defines the interface that isolates all PSP (Nomba) calls.
// Nothing outside this package knows Nomba exists.
package provider

import (
	"context"
	"time"
)

// Provider is the single abstraction over the payment provider (Nomba in v1).
// CI and the harness swap in fakeprov; production uses the nomba adapter.
type Provider interface {
	// Virtual account lifecycle
	CreateVA(ctx context.Context, req CreateVARequest) (*VAResponse, error)
	UpdateVA(ctx context.Context, identifier, newName string) error
	CloseVA(ctx context.Context, identifier string) error
	SuspendVA(ctx context.Context, accountID string) error

	// Transaction data
	ListTransactions(ctx context.Context, req ListTransactionsRequest) (*TransactionPage, error)
	Requery(ctx context.Context, sessionID string) (*ProviderTransaction, error)

	// Webhook handling
	VerifyWebhookSignature(ctx context.Context, headers map[string]string, body []byte) error
	ParseWebhook(ctx context.Context, body []byte) (*WebhookPayload, error)
}

// ── Request / response types ──────────────────────────────────────────────────

type CreateVARequest struct {
	AccountRef  string // deterministic: t{tenantShort}c{customerULID}
	AccountName string // 8–64 chars, derived from identity
	BVN         string // optional
}

type VAResponse struct {
	AccountRef    string
	NUBAN         string
	BankName      string
	AccountName   string
	AccountHolderID string
}

type ListTransactionsRequest struct {
	DateFrom  time.Time
	DateTo    time.Time
	Cursor    string // opaque pagination cursor returned by Nomba
	PageSize  int
}

type TransactionPage struct {
	Transactions []ProviderTransaction
	NextCursor   string // empty when exhausted
}

type ProviderTransaction struct {
	TransactionID string
	SessionID     string
	AccountNumber string // NUBAN that received the funds
	AccountRef    string
	AmountKobo    int64
	Type          string // "vact_transfer" etc.
	Status        string
	SenderName    string
	SenderBank    string
	Narration     string
	OccurredAt    time.Time
	Raw           []byte // full JSON from Nomba
}

// ── Webhook payload ───────────────────────────────────────────────────────────

type WebhookPayload struct {
	EventType   string              // "payment_success" | "payment_reversal" | "payment_failed"
	Transaction ProviderTransaction
	Raw         []byte
}
