// Package domain holds pure entity types and invariants. No I/O.
package domain

import (
	"errors"
	"time"
)

// ── Tenant & auth ─────────────────────────────────────────────────────────────

type Tenant struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

type APIKey struct {
	ID        string
	TenantID  string
	RawKey    string // stored for demo login; empty for keys created before migration 018
	KeyHash   string
	KeyPrefix string
	Active    bool
	CreatedAt time.Time
}

// UserCredential is a human login tied to a tenant (or nil tenant_id for platform admin).
type UserCredential struct {
	ID           string
	TenantID     *string // nil for platform admin
	Email        string
	PasswordHash string
	Name         string
	Role         string // "dev" | "ops" | "admin"
	CreatedAt    time.Time
}

// ── Customer & identity ───────────────────────────────────────────────────────

type Customer struct {
	ID          string
	TenantID    string
	ExternalRef string
	DisplayName string
	Status      string // "active" | "inactive"
	Identity    *Identity
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Identity struct {
	ID                 string
	CustomerID         string
	BVNMasked          *string
	NINMasked          *string
	KYCTier            int // 1, 2, or 3
	VerificationStatus string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// IdentityInput is used when creating a customer.
type IdentityInput struct {
	BVNMasked *string
	NINMasked *string
	KYCTier   int
}

// Validate checks the CBN BVN-or-NIN rule (FR-2.3).
func (i IdentityInput) Validate() error {
	if i.BVNMasked == nil && i.NINMasked == nil {
		return errors.New("at least one of BVN or NIN must be provided")
	}
	if i.KYCTier < 1 || i.KYCTier > 3 {
		return errors.New("kyc_tier must be 1, 2, or 3")
	}
	return nil
}

// ── Virtual account ───────────────────────────────────────────────────────────

type VirtualAccount struct {
	ID              string
	CustomerID      string
	NombaAccountRef string
	NUBAN           string
	BankName        string
	AccountName     string
	NombaHolderID   string
	Status          VAStatus
	CreatedAt       time.Time
	UpdatedAt       time.Time
	// Enriched from the linked customer/tenant (populated by list queries)
	CustomerDisplayName string
	TenantName          string
}

type VAStatus string

const (
	VAStatusPending   VAStatus = "PENDING"
	VAStatusActive    VAStatus = "ACTIVE"
	VAStatusSuspended VAStatus = "SUSPENDED"
	VAStatusClosed    VAStatus = "CLOSED"
)

// ValidTransition returns true if moving from current to next is a legal state transition (FR-8.5).
func (s VAStatus) ValidTransition(next VAStatus) bool {
	allowed := map[VAStatus][]VAStatus{
		VAStatusPending:   {VAStatusActive},
		VAStatusActive:    {VAStatusSuspended, VAStatusClosed},
		VAStatusSuspended: {VAStatusActive, VAStatusClosed},
		VAStatusClosed:    {},
	}
	for _, a := range allowed[s] {
		if a == next {
			return true
		}
	}
	return false
}

// ── Transactions & ledger ─────────────────────────────────────────────────────

type Transaction struct {
	ID               string
	VirtualAccountID *string
	SessionID        *string
	AmountKobo       int64
	Direction        string // "credit" | "debit"
	Source           string // "webhook" | "sweep"
	Status           string // "posted" | "reversed" | "pending"
	SenderName       *string
	SenderBank       *string
	Narration        *string
	Raw              []byte
	OccurredAt       time.Time
	CreatedAt        time.Time
	// Enriched from the linked virtual account (populated by ListTenantTransactions)
	NUBAN string
}

type LedgerEntry struct {
	TransactionID string
	Account       string // e.g. "customer_wallet:uuid", "nomba_settlement", "suspense"
	Direction     string // "debit" | "credit"
	AmountKobo    int64
}

// LedgerAccount helpers
func CustomerWalletAccount(customerID string) string {
	return "customer_wallet:" + customerID
}

const (
	AccountNombaSettlement = "nomba_settlement"
	AccountSuspense        = "suspense"
	AccountFeeIncome       = "fee_income"
)

// Statement is the output of GET /statement.
type Statement struct {
	OpeningBalanceKobo int64
	ClosingBalanceKobo int64
	From               time.Time
	To                 time.Time
	Entries            []StatementEntry
}

type StatementEntry struct {
	OccurredAt     time.Time
	Description    string
	DebitKobo      int64
	CreditKobo     int64
	RunningBalance int64
}

// ── Webhook events ────────────────────────────────────────────────────────────

type WebhookEvent struct {
	ID             string
	DedupeKey      string // transactionId + ":" + event_type
	EventType      string
	SignatureValid  bool
	Status         string
	Payload        []byte
	CreatedAt      time.Time
	ProcessedAt    *time.Time
}

// ── Suspense ──────────────────────────────────────────────────────────────────

type SuspenseItem struct {
	ID            string
	TransactionID string
	Reason        SuspenseReason
	Status        string // "open" | "reassigned" | "refund_flagged"
	ResolvedBy    *string
	ResolvedAt    *time.Time
	Notes         *string
	CreatedAt     time.Time
	// Enriched from the linked transaction + virtual account (populated by ListSuspenseItems)
	AmountKobo int64
	NUBAN      string
}

// CrossTenantSuspenseItem is a SuspenseItem enriched with tenant name (admin view).
type CrossTenantSuspenseItem struct {
	SuspenseItem
	TenantName string
}

type SuspenseReason string

const (
	SuspenseReasonUnmatched        SuspenseReason = "unmatched"
	SuspenseReasonClosedAccount    SuspenseReason = "closed_account"
	SuspenseReasonSuspendedAccount SuspenseReason = "suspended_account"
	SuspenseReasonAmountMismatch   SuspenseReason = "amount_mismatch"
	SuspenseReasonTierLimit        SuspenseReason = "tier_limit"
)

// ── Sweep ─────────────────────────────────────────────────────────────────────

type SweepRun struct {
	ID           string
	WindowFrom   time.Time
	WindowTo     time.Time
	PagesFetched int
	Found        int
	Posted       int
	Suspensed    int
	DurationMS   *int
	Error        *string
	RanAt        time.Time
}

// ── Relay (FR-11) ─────────────────────────────────────────────────────────────

// RelayEndpoint is a tenant-registered URL that receives payment event webhooks.
type RelayEndpoint struct {
	ID         string
	TenantID   string
	URL        string
	SecretHash string // SHA-256 hex of the signing secret (not stored in plaintext)
	Active     bool
	CreatedAt  time.Time
}

// RelayDelivery is one fan-out attempt to a RelayEndpoint.
type RelayDelivery struct {
	ID          string
	EndpointID  string
	EventType   string
	Payload     []byte // JSON
	Attempt     int
	Status      string // pending | delivered | failed | dead_letter
	StatusCode  *int
	Error       *string
	NextRetryAt *time.Time
	DeliveredAt *time.Time
	CreatedAt   time.Time
}

// ── Platform health (GlobalHealth dashboard) ──────────────────────────────────

type PlatformHealth struct {
	Ledger              LedgerHealth
	LastSweep           *SweepHealthSnapshot // nil if no sweep has ever run
	Webhook24h          WebhookHealth
	CrossTenantSuspense SuspenseHealth
	ActiveTenants       int
	TotalTenants        int
	TenantHealth        []TenantHealth
	CheckedAt           time.Time
}

type LedgerHealth struct {
	DebitsKobo  int64
	CreditsKobo int64
	Balanced    bool
}

type SweepHealthSnapshot struct {
	Posted    int
	Found     int
	Suspensed int
	RanAt     time.Time
}

type WebhookHealth struct {
	Delivered int64
	Total     int64
}

type SuspenseHealth struct {
	AmountKobo  int64
	ItemCount   int64
	TenantCount int64
}

type TenantHealth struct {
	ID               string
	Name             string
	Customers        int64
	Accounts         int64
	OpenSuspenseKobo int64
	LastActivity     *time.Time
	Status           string
}

// ── Audit ─────────────────────────────────────────────────────────────────────

type AuditEntry struct {
	ID          string
	TenantID    *string
	Actor       string
	Action      string
	EntityType  string
	EntityID    string
	BeforeAfter []byte // JSON
	At          time.Time
}
