// Package store defines repository interfaces for every aggregate.
// Concrete implementations live in store/postgres.
package store

import (
	"context"
	"time"

	"github.com/systynlabs/vaultnuban/internal/domain"
)

// TenantStore manages tenant and API-key records.
type TenantStore interface {
	CreateTenant(ctx context.Context, name string) (*domain.Tenant, error)
	GetTenantByAPIKey(ctx context.Context, keyHash string) (*domain.Tenant, *domain.APIKey, error)
}

// CustomerStore manages customers and their identity records.
type CustomerStore interface {
	CreateCustomer(ctx context.Context, tenantID, externalRef, displayName string, identity domain.IdentityInput) (*domain.Customer, error)
	GetCustomer(ctx context.Context, tenantID, customerID string) (*domain.Customer, error)
	GetCustomerByExternalRef(ctx context.Context, tenantID, externalRef string) (*domain.Customer, error)
	UpdateKYCTier(ctx context.Context, customerID string, newTier int, actor string) error
}

// VirtualAccountStore manages virtual account lifecycle.
type VirtualAccountStore interface {
	CreateVirtualAccount(ctx context.Context, va *domain.VirtualAccount) error
	GetActiveVA(ctx context.Context, customerID string) (*domain.VirtualAccount, error)
	GetVAByNUBAN(ctx context.Context, nuban string) (*domain.VirtualAccount, error)
	GetVAByAccountRef(ctx context.Context, accountRef string) (*domain.VirtualAccount, error)
	GetVAByCustomerAndStatus(ctx context.Context, customerID, status string) (*domain.VirtualAccount, error)
	UpdateVAStatus(ctx context.Context, vaID, status, actor string) error
	RenameVA(ctx context.Context, vaID, newName, actor string) error
}

// TransactionStore manages inbound transaction records and ledger entries.
type TransactionStore interface {
	// PostTransaction inserts the transaction and its balanced ledger entries atomically.
	// Returns created=false (no error) if the transactionId already exists (idempotent).
	PostTransaction(ctx context.Context, tx *domain.Transaction, entries []domain.LedgerEntry) (created bool, err error)
	GetTransaction(ctx context.Context, txID string) (*domain.Transaction, error)
	ListTransactions(ctx context.Context, vaID string, limit int, cursor string) ([]*domain.Transaction, string, error)
	GetBalance(ctx context.Context, customerWalletAccount string) (int64, error)
	GetDailyCredits(ctx context.Context, customerWalletAccount string, date time.Time) (int64, error)
	GetStatement(ctx context.Context, customerWalletAccount string, from, to time.Time) (*domain.Statement, error)
}

// WebhookEventStore manages webhook event deduplication.
type WebhookEventStore interface {
	InsertWebhookEvent(ctx context.Context, evt *domain.WebhookEvent) (inserted bool, err error)
	MarkWebhookProcessed(ctx context.Context, id, status string) error
}

// SuspenseStore manages suspense items and their resolution.
type SuspenseStore interface {
	CreateSuspenseItem(ctx context.Context, item *domain.SuspenseItem) error
	ListSuspenseItems(ctx context.Context, tenantID string, limit int, cursor string) ([]*domain.SuspenseItem, string, error)
	ResolveSuspenseItem(ctx context.Context, itemID, resolution, actor, notes string) error
	GetSuspenseItem(ctx context.Context, itemID string) (*domain.SuspenseItem, error)
}

// SweepStore records sweep run metadata.
type SweepStore interface {
	CreateSweepRun(ctx context.Context, run *domain.SweepRun) error
	GetLastSweepTime(ctx context.Context) (time.Time, error)
}

// AuditStore appends immutable audit entries.
type AuditStore interface {
	Append(ctx context.Context, entry *domain.AuditEntry) error
}
