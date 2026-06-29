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
	CreateAPIKey(ctx context.Context, tenantID, rawKey, keyHash, keyPrefix string) (*domain.APIKey, error)
	ListTenants(ctx context.Context) ([]*domain.Tenant, error)
	ListAPIKeys(ctx context.Context, tenantID string) ([]*domain.APIKey, error)
	RevokeAPIKey(ctx context.Context, keyID, tenantID string) error
}

// CustomerStore manages customers and their identity records.
type CustomerStore interface {
	CreateCustomer(ctx context.Context, tenantID, externalRef, displayName string, identity domain.IdentityInput) (*domain.Customer, error)
	GetCustomer(ctx context.Context, tenantID, customerID string) (*domain.Customer, error)
	GetCustomerByExternalRef(ctx context.Context, tenantID, externalRef string) (*domain.Customer, error)
	UpdateKYCTier(ctx context.Context, customerID string, newTier int, actor string) error
	ListCustomers(ctx context.Context, tenantID string, limit int, cursor string) ([]*domain.Customer, string, error)
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
	ListVAs(ctx context.Context, tenantID string, limit int, cursor string) ([]*domain.VirtualAccount, string, error)
}

// TransactionStore manages inbound transaction records and ledger entries.
type TransactionStore interface {
	// PostTransaction inserts the transaction and its balanced ledger entries atomically.
	// Returns created=false (no error) if the transactionId already exists (idempotent).
	PostTransaction(ctx context.Context, tx *domain.Transaction, entries []domain.LedgerEntry) (created bool, err error)
	GetTransaction(ctx context.Context, txID string) (*domain.Transaction, error)
	// GetTransactionForTenant fetches a single transaction and validates it belongs to the tenant.
	GetTransactionForTenant(ctx context.Context, tenantID, txID string) (*domain.Transaction, error)
	ListTransactions(ctx context.Context, vaID string, limit int, cursor string) ([]*domain.Transaction, string, error)
	ListTenantTransactions(ctx context.Context, tenantID string, limit int, cursor string) ([]*domain.Transaction, string, error)
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
	ListSweepRuns(ctx context.Context, limit int) ([]*domain.SweepRun, error)
}

// AuditStore appends immutable audit entries.
type AuditStore interface {
	Append(ctx context.Context, entry *domain.AuditEntry) error
	ListAuditEntries(ctx context.Context, tenantID string, limit int, cursor string) ([]*domain.AuditEntry, string, error)
}

// AuthStore manages human user credentials for the dashboard login.
type AuthStore interface {
	CreateCredential(ctx context.Context, cred *domain.UserCredential) error
	// SeedCredential inserts the credential only if the email does not already exist.
	SeedCredential(ctx context.Context, cred *domain.UserCredential) error
	// GetCredentialByEmail returns the credential, its tenant (nil for admin), and the tenant's active API key (nil for admin).
	GetCredentialByEmail(ctx context.Context, email string) (*domain.UserCredential, *domain.Tenant, *domain.APIKey, error)
}

// SettingsStore manages application-wide configuration stored in the database.
type SettingsStore interface {
	// GetSetting returns the raw JSON value for a key, or nil if not set.
	GetSetting(ctx context.Context, key string) ([]byte, error)
	// UpsertSetting inserts or replaces the value for a key.
	UpsertSetting(ctx context.Context, key string, value []byte) error
	// SeedSetting inserts the value only if the key does not already exist.
	SeedSetting(ctx context.Context, key string, value []byte) error
}

// PlatformHealthStore aggregates platform-wide health metrics for the admin dashboard.
type PlatformHealthStore interface {
	GetPlatformHealth(ctx context.Context) (*domain.PlatformHealth, error)
	ListCrossTenantSuspense(ctx context.Context, limit int, cursor string) ([]*domain.CrossTenantSuspenseItem, string, error)
}

// RelayStore manages tenant webhook relay endpoints and delivery records (FR-11).
type RelayStore interface {
	CreateEndpoint(ctx context.Context, ep *domain.RelayEndpoint) error
	ListEndpoints(ctx context.Context, tenantID string) ([]*domain.RelayEndpoint, error)
	GetEndpoint(ctx context.Context, id string) (*domain.RelayEndpoint, error)
	DeactivateEndpoint(ctx context.Context, id, tenantID string) error

	CreateDelivery(ctx context.Context, d *domain.RelayDelivery) error
	UpdateDelivery(ctx context.Context, d *domain.RelayDelivery) error
	GetDelivery(ctx context.Context, id string) (*domain.RelayDelivery, error)
	// ListDeliveries returns all delivery attempts for a tenant, newest first.
	ListDeliveries(ctx context.Context, tenantID string, limit int, cursor string) ([]*domain.RelayDelivery, string, error)
	// ListPendingRetries returns failed deliveries whose next_retry_at is in the past.
	ListPendingRetries(ctx context.Context, limit int) ([]*domain.RelayDelivery, error)
}
