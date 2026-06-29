// Package memstore provides in-memory implementations of every store interface.
// It is intended for use in harness tests and unit tests only — not production.
package memstore

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/systynlabs/vaultnuban/internal/domain"
)

// ── Counters for synthetic IDs ────────────────────────────────────────────────

var (
	idSeq       uint64
	webhookSeq  uint64
	suspenseSeq uint64
	sweepSeq    uint64
)

func nextID(prefix string) string {
	n := atomic.AddUint64(&idSeq, 1)
	return fmt.Sprintf("%s-%04d", prefix, n)
}

// ── TenantStore ───────────────────────────────────────────────────────────────

type TenantStore struct {
	mu      sync.Mutex
	tenants map[string]*domain.Tenant   // id → tenant
	keys    map[string]*domain.APIKey   // hash → key
	byKey   map[string]string           // hash → tenantID
}

func NewTenantStore() *TenantStore {
	return &TenantStore{
		tenants: make(map[string]*domain.Tenant),
		keys:    make(map[string]*domain.APIKey),
		byKey:   make(map[string]string),
	}
}

func (s *TenantStore) CreateTenant(_ context.Context, name string) (*domain.Tenant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &domain.Tenant{ID: nextID("tenant"), Name: name, CreatedAt: time.Now()}
	s.tenants[t.ID] = t
	return t, nil
}

func (s *TenantStore) GetTenantByAPIKey(_ context.Context, keyHash string) (*domain.Tenant, *domain.APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tid, ok := s.byKey[keyHash]
	if !ok {
		return nil, nil, nil
	}
	key := s.keys[keyHash]
	return s.tenants[tid], key, nil
}

func (s *TenantStore) CreateAPIKey(_ context.Context, tenantID, rawKey, keyHash, keyPrefix string) (*domain.APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := &domain.APIKey{ID: nextID("key"), TenantID: tenantID, RawKey: rawKey, KeyHash: keyHash, KeyPrefix: keyPrefix, Active: true}
	s.keys[keyHash] = k
	s.byKey[keyHash] = tenantID
	return k, nil
}

// SeedAPIKey registers a key hash → tenant mapping for tests.
func (s *TenantStore) SeedAPIKey(tenantID, keyHash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[keyHash] = &domain.APIKey{ID: nextID("key"), TenantID: tenantID, KeyHash: keyHash, KeyPrefix: "test_"}
	s.byKey[keyHash] = tenantID
}

// ── CustomerStore ─────────────────────────────────────────────────────────────

type CustomerStore struct {
	mu          sync.Mutex
	customers   map[string]*domain.Customer // id → customer
	byExtRef    map[string]string           // tenantID+":"+externalRef → id
}

func NewCustomerStore() *CustomerStore {
	return &CustomerStore{
		customers: make(map[string]*domain.Customer),
		byExtRef:  make(map[string]string),
	}
}

func (s *CustomerStore) CreateCustomer(
	_ context.Context,
	tenantID, externalRef, displayName string,
	identity domain.IdentityInput,
) (*domain.Customer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := nextID("cust")
	c := &domain.Customer{
		ID:          id,
		TenantID:    tenantID,
		ExternalRef: externalRef,
		DisplayName: displayName,
		Identity: &domain.Identity{
			CustomerID: id,
			BVNMasked:  identity.BVNMasked,
			NINMasked:  identity.NINMasked,
			KYCTier:    identity.KYCTier,
		},
		CreatedAt: time.Now(),
	}
	s.customers[id] = c
	s.byExtRef[tenantID+":"+externalRef] = id
	return c, nil
}

func (s *CustomerStore) GetCustomer(_ context.Context, tenantID, customerID string) (*domain.Customer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.customers[customerID]
	if !ok {
		return nil, nil
	}
	// Empty tenantID = cross-tenant lookup (used by recon worker)
	if tenantID != "" && c.TenantID != tenantID {
		return nil, nil
	}
	return c, nil
}

func (s *CustomerStore) GetCustomerByExternalRef(_ context.Context, tenantID, externalRef string) (*domain.Customer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byExtRef[tenantID+":"+externalRef]
	if !ok {
		return nil, nil
	}
	return s.customers[id], nil
}

func (s *CustomerStore) UpdateKYCTier(_ context.Context, customerID string, newTier int, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.customers[customerID]
	if !ok {
		return fmt.Errorf("memstore: customer not found: %s", customerID)
	}
	if c.Identity == nil {
		c.Identity = &domain.Identity{CustomerID: customerID}
	}
	c.Identity.KYCTier = newTier
	return nil
}

func (s *CustomerStore) ListCustomers(_ context.Context, tenantID string, limit int, _ string) ([]*domain.Customer, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*domain.Customer
	for _, c := range s.customers {
		if c.TenantID == tenantID {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, "", nil
}

// ── AuthStore ─────────────────────────────────────────────────────────────────

type AuthStore struct {
	mu    sync.Mutex
	creds map[string]*domain.UserCredential // email → cred
}

func NewAuthStore() *AuthStore {
	return &AuthStore{creds: make(map[string]*domain.UserCredential)}
}

func (s *AuthStore) CreateCredential(_ context.Context, cred *domain.UserCredential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := *cred
	c.ID = nextID("cred")
	s.creds[cred.Email] = &c
	return nil
}

func (s *AuthStore) SeedCredential(_ context.Context, cred *domain.UserCredential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.creds[cred.Email]; exists {
		return nil
	}
	c := *cred
	c.ID = nextID("cred")
	s.creds[cred.Email] = &c
	return nil
}

func (s *AuthStore) GetCredentialByEmail(_ context.Context, email string) (*domain.UserCredential, *domain.Tenant, *domain.APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cred, ok := s.creds[email]
	if !ok {
		return nil, nil, nil, nil
	}
	return cred, nil, nil, nil
}

// ── VirtualAccountStore ───────────────────────────────────────────────────────

type VirtualAccountStore struct {
	mu       sync.Mutex
	accounts map[string]*domain.VirtualAccount // id → va
	byNUBAN  map[string]string                 // nuban → id
	byRef    map[string]string                 // accountRef → id
}

func NewVirtualAccountStore() *VirtualAccountStore {
	return &VirtualAccountStore{
		accounts: make(map[string]*domain.VirtualAccount),
		byNUBAN:  make(map[string]string),
		byRef:    make(map[string]string),
	}
}

func (s *VirtualAccountStore) CreateVirtualAccount(_ context.Context, va *domain.VirtualAccount) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if va.ID == "" {
		va.ID = nextID("va")
	}
	va.CreatedAt = time.Now()
	s.accounts[va.ID] = va
	s.byNUBAN[va.NUBAN] = va.ID
	s.byRef[va.NombaAccountRef] = va.ID
	return nil
}

func (s *VirtualAccountStore) GetActiveVA(_ context.Context, customerID string) (*domain.VirtualAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, va := range s.accounts {
		if va.CustomerID == customerID && va.Status == domain.VAStatusActive {
			return va, nil
		}
	}
	return nil, nil
}

func (s *VirtualAccountStore) GetVAByNUBAN(_ context.Context, nuban string) (*domain.VirtualAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byNUBAN[nuban]
	if !ok {
		return nil, nil
	}
	return s.accounts[id], nil
}

func (s *VirtualAccountStore) GetVAByAccountRef(_ context.Context, accountRef string) (*domain.VirtualAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byRef[accountRef]
	if !ok {
		return nil, nil
	}
	return s.accounts[id], nil
}

func (s *VirtualAccountStore) GetVAByCustomerAndStatus(_ context.Context, customerID, status string) (*domain.VirtualAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, va := range s.accounts {
		if va.CustomerID == customerID && string(va.Status) == status {
			return va, nil
		}
	}
	return nil, nil
}

func (s *VirtualAccountStore) UpdateVAStatus(_ context.Context, vaID, status, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	va, ok := s.accounts[vaID]
	if !ok {
		return fmt.Errorf("memstore: VA not found: %s", vaID)
	}
	va.Status = domain.VAStatus(status)
	return nil
}

func (s *VirtualAccountStore) RenameVA(_ context.Context, vaID, newName, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	va, ok := s.accounts[vaID]
	if !ok {
		return fmt.Errorf("memstore: VA not found: %s", vaID)
	}
	va.AccountName = newName
	return nil
}

// ── TransactionStore ──────────────────────────────────────────────────────────

// ledgerRow holds a single ledger entry with its transaction's occurred_at for ordering.
type ledgerRow struct {
	domain.LedgerEntry
	occurredAt time.Time
}

type TransactionStore struct {
	mu       sync.Mutex
	txns     map[string]*domain.Transaction // txnID → tx
	ledger   []ledgerRow
}

func NewTransactionStore() *TransactionStore {
	return &TransactionStore{
		txns:   make(map[string]*domain.Transaction),
		ledger: nil,
	}
}

func (s *TransactionStore) PostTransaction(
	_ context.Context,
	tx *domain.Transaction,
	entries []domain.LedgerEntry,
) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.txns[tx.ID]; exists {
		return false, nil // idempotent
	}

	// NFR-1: verify balance before accepting
	var sum int64
	for _, e := range entries {
		if e.Direction == "credit" {
			sum += e.AmountKobo
		} else {
			sum -= e.AmountKobo
		}
	}
	if sum != 0 {
		return false, fmt.Errorf("memstore: unbalanced ledger entries for txn %s: net=%d", tx.ID, sum)
	}

	if tx.CreatedAt.IsZero() {
		tx.CreatedAt = time.Now()
	}
	s.txns[tx.ID] = tx
	for _, e := range entries {
		s.ledger = append(s.ledger, ledgerRow{LedgerEntry: e, occurredAt: tx.OccurredAt})
	}
	return true, nil
}

func (s *TransactionStore) GetTransaction(_ context.Context, txID string) (*domain.Transaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, ok := s.txns[txID]
	if !ok {
		return nil, nil
	}
	return tx, nil
}

func (s *TransactionStore) ListTransactions(
	_ context.Context,
	vaID string,
	limit int,
	cursor string,
) ([]*domain.Transaction, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var all []*domain.Transaction
	for _, tx := range s.txns {
		if tx.VirtualAccountID != nil && *tx.VirtualAccountID == vaID {
			all = append(all, tx)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].OccurredAt.After(all[j].OccurredAt)
	})

	// Cursor: skip until we find the cursor txnID, then take the rest.
	start := 0
	if cursor != "" {
		for i, tx := range all {
			if tx.ID == cursor {
				start = i + 1
				break
			}
		}
	}
	all = all[start:]
	if limit <= 0 {
		limit = 50
	}

	var nextCursor string
	if len(all) > limit {
		nextCursor = all[limit-1].ID
		all = all[:limit]
	}
	return all, nextCursor, nil
}

func (s *TransactionStore) GetBalance(_ context.Context, walletAccount string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var bal int64
	for _, row := range s.ledger {
		if row.Account == walletAccount {
			if row.Direction == "credit" {
				bal += row.AmountKobo
			} else {
				bal -= row.AmountKobo
			}
		}
	}
	return bal, nil
}

func (s *TransactionStore) GetDailyCredits(_ context.Context, walletAccount string, date time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	dayStart := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	dayEnd := dayStart.Add(24 * time.Hour)

	var total int64
	for _, row := range s.ledger {
		if row.Account == walletAccount &&
			row.Direction == "credit" &&
			(row.occurredAt.Equal(dayStart) || row.occurredAt.After(dayStart)) &&
			row.occurredAt.Before(dayEnd) {
			total += row.AmountKobo
		}
	}
	return total, nil
}

func (s *TransactionStore) GetStatement(
	_ context.Context,
	walletAccount string,
	from, to time.Time,
) (*domain.Statement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Opening balance: all entries before `from`.
	var opening int64
	for _, row := range s.ledger {
		if row.Account == walletAccount && row.occurredAt.Before(from) {
			if row.Direction == "credit" {
				opening += row.AmountKobo
			} else {
				opening -= row.AmountKobo
			}
		}
	}

	// Collect in-window entries.
	var inWindow []ledgerRow
	for _, row := range s.ledger {
		if row.Account == walletAccount &&
			(row.occurredAt.Equal(from) || row.occurredAt.After(from)) &&
			row.occurredAt.Before(to) {
			inWindow = append(inWindow, row)
		}
	}
	sort.Slice(inWindow, func(i, j int) bool {
		return inWindow[i].occurredAt.Before(inWindow[j].occurredAt)
	})

	running := opening
	entries := make([]domain.StatementEntry, 0, len(inWindow))
	for _, row := range inWindow {
		e := domain.StatementEntry{OccurredAt: row.occurredAt}
		if row.Direction == "credit" {
			e.CreditKobo = row.AmountKobo
			running += row.AmountKobo
		} else {
			e.DebitKobo = row.AmountKobo
			running -= row.AmountKobo
		}
		e.RunningBalance = running
		entries = append(entries, e)
	}

	return &domain.Statement{
		OpeningBalanceKobo: opening,
		ClosingBalanceKobo: running,
		From:               from,
		To:                 to,
		Entries:            entries,
	}, nil
}

// AllLedgerEntries returns a snapshot of every ledger entry — used by NFR-1 checks.
func (s *TransactionStore) AllLedgerEntries() []domain.LedgerEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.LedgerEntry, len(s.ledger))
	for i, r := range s.ledger {
		out[i] = r.LedgerEntry
	}
	return out
}

// ── WebhookEventStore ─────────────────────────────────────────────────────────

type WebhookEventStore struct {
	mu     sync.Mutex
	events map[string]*domain.WebhookEvent // dedupeKey → event
	byID   map[string]*domain.WebhookEvent // id → event
}

func NewWebhookEventStore() *WebhookEventStore {
	return &WebhookEventStore{
		events: make(map[string]*domain.WebhookEvent),
		byID:   make(map[string]*domain.WebhookEvent),
	}
}

func (s *WebhookEventStore) InsertWebhookEvent(_ context.Context, evt *domain.WebhookEvent) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.events[evt.DedupeKey]; exists {
		return false, nil
	}
	if evt.ID == "" {
		n := atomic.AddUint64(&webhookSeq, 1)
		evt.ID = fmt.Sprintf("wh-%04d", n)
	}
	evt.CreatedAt = time.Now()
	s.events[evt.DedupeKey] = evt
	s.byID[evt.ID] = evt
	return true, nil
}

func (s *WebhookEventStore) MarkWebhookProcessed(_ context.Context, id, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	evt, ok := s.byID[id]
	if !ok {
		return nil // no-op if not found
	}
	evt.Status = status
	return nil
}

// ── SuspenseStore ─────────────────────────────────────────────────────────────

type SuspenseStore struct {
	mu    sync.Mutex
	items map[string]*domain.SuspenseItem // id → item
}

func NewSuspenseStore() *SuspenseStore {
	return &SuspenseStore{items: make(map[string]*domain.SuspenseItem)}
}

func (s *SuspenseStore) CreateSuspenseItem(_ context.Context, item *domain.SuspenseItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item.ID == "" {
		n := atomic.AddUint64(&suspenseSeq, 1)
		item.ID = fmt.Sprintf("susp-%04d", n)
	}
	item.CreatedAt = time.Now()
	s.items[item.ID] = item
	return nil
}

func (s *SuspenseStore) GetSuspenseItem(_ context.Context, itemID string) (*domain.SuspenseItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[itemID]
	if !ok {
		return nil, nil
	}
	return item, nil
}

func (s *SuspenseStore) ListSuspenseItems(
	_ context.Context,
	_ string,
	limit int,
	_ string,
) ([]*domain.SuspenseItem, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*domain.SuspenseItem
	for _, item := range s.items {
		if item.Status == "open" {
			out = append(out, item)
		}
	}
	if limit > 0 && len(out) > limit {
		return out[:limit], out[limit-1].ID, nil
	}
	return out, "", nil
}

func (s *SuspenseStore) ResolveSuspenseItem(_ context.Context, itemID, resolution, actor, notes string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[itemID]
	if !ok {
		return fmt.Errorf("memstore: suspense item not found: %s", itemID)
	}
	item.Status = resolution
	item.ResolvedBy = &actor
	item.Notes = &notes
	return nil
}

// OpenItems returns all open suspense items — used in test assertions.
func (s *SuspenseStore) OpenItems() []*domain.SuspenseItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*domain.SuspenseItem
	for _, item := range s.items {
		if item.Status == "open" {
			out = append(out, item)
		}
	}
	return out
}

// ── SweepStore ────────────────────────────────────────────────────────────────

type SweepStore struct {
	mu   sync.Mutex
	runs []*domain.SweepRun
}

func NewSweepStore() *SweepStore { return &SweepStore{} }

func (s *SweepStore) CreateSweepRun(_ context.Context, run *domain.SweepRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := atomic.AddUint64(&sweepSeq, 1)
	run.ID = fmt.Sprintf("sweep-%04d", n)
	s.runs = append(s.runs, run)
	return nil
}

func (s *SweepStore) GetLastSweepTime(_ context.Context) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.runs) == 0 {
		return time.Time{}, nil
	}
	return s.runs[len(s.runs)-1].WindowTo, nil
}

func (s *SweepStore) ListSweepRuns(_ context.Context, limit int) ([]*domain.SweepRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 50
	}
	out := make([]*domain.SweepRun, 0, len(s.runs))
	for i := len(s.runs) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.runs[i])
	}
	return out, nil
}

// ── AuditStore ────────────────────────────────────────────────────────────────

type AuditStore struct {
	mu      sync.Mutex
	entries []*domain.AuditEntry
}

func NewAuditStore() *AuditStore { return &AuditStore{} }

func (s *AuditStore) Append(_ context.Context, entry *domain.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
	return nil
}

func (s *AuditStore) ListAuditEntries(_ context.Context, _ string, _ int, _ string) ([]*domain.AuditEntry, string, error) {
	return nil, "", nil
}

// ── RelayStore ────────────────────────────────────────────────────────────────

type RelayStore struct {
	mu        sync.Mutex
	endpoints map[string]*domain.RelayEndpoint
	deliveries []*domain.RelayDelivery
}

func NewRelayStore() *RelayStore {
	return &RelayStore{endpoints: make(map[string]*domain.RelayEndpoint)}
}

func (s *RelayStore) CreateEndpoint(_ context.Context, ep *domain.RelayEndpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ep.ID == "" {
		ep.ID = nextID("ep")
	}
	ep.CreatedAt = time.Now()
	s.endpoints[ep.ID] = ep
	return nil
}

func (s *RelayStore) ListEndpoints(_ context.Context, tenantID string) ([]*domain.RelayEndpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*domain.RelayEndpoint
	for _, ep := range s.endpoints {
		if ep.TenantID == tenantID {
			out = append(out, ep)
		}
	}
	return out, nil
}

func (s *RelayStore) GetEndpoint(_ context.Context, id string) (*domain.RelayEndpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ep, ok := s.endpoints[id]
	if !ok {
		return nil, nil
	}
	return ep, nil
}

func (s *RelayStore) DeactivateEndpoint(_ context.Context, id, tenantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ep, ok := s.endpoints[id]
	if !ok || ep.TenantID != tenantID {
		return fmt.Errorf("memstore: relay endpoint not found")
	}
	ep.Active = false
	return nil
}

func (s *RelayStore) CreateDelivery(_ context.Context, d *domain.RelayDelivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.ID == "" {
		d.ID = nextID("del")
	}
	d.CreatedAt = time.Now()
	s.deliveries = append(s.deliveries, d)
	return nil
}

func (s *RelayStore) UpdateDelivery(_ context.Context, d *domain.RelayDelivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.deliveries {
		if existing.ID == d.ID {
			s.deliveries[i] = d
			return nil
		}
	}
	return fmt.Errorf("memstore: delivery not found: %s", d.ID)
}

func (s *RelayStore) ListPendingRetries(_ context.Context, limit int) ([]*domain.RelayDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var out []*domain.RelayDelivery
	for _, d := range s.deliveries {
		if d.Status == "failed" && d.NextRetryAt != nil && !d.NextRetryAt.After(now) {
			out = append(out, d)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

// ── SettingsStore ─────────────────────────────────────────────────────────────

type SettingsStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func NewSettingsStore() *SettingsStore {
	return &SettingsStore{data: make(map[string][]byte)}
}

func (s *SettingsStore) GetSetting(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[key]
	if !ok {
		return nil, nil
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (s *SettingsStore) UpsertSetting(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]byte, len(value))
	copy(cp, value)
	s.data[key] = cp
	return nil
}

func (s *SettingsStore) SeedSetting(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data[key]; exists {
		return nil
	}
	cp := make([]byte, len(value))
	copy(cp, value)
	s.data[key] = cp
	return nil
}

// ── HealthStore ───────────────────────────────────────────────────────────────

type HealthStore struct{}

func NewHealthStore() *HealthStore { return &HealthStore{} }

func (s *HealthStore) GetPlatformHealth(_ context.Context) (*domain.PlatformHealth, error) {
	return &domain.PlatformHealth{}, nil
}

func (s *HealthStore) ListCrossTenantSuspense(_ context.Context, _ int, _ string) ([]*domain.CrossTenantSuspenseItem, string, error) {
	return nil, "", nil
}

// ── New list stubs required by updated store interfaces ───────────────────────

func (s *TenantStore) ListTenants(_ context.Context) ([]*domain.Tenant, error) {
	return nil, nil
}

func (s *TenantStore) ListAPIKeys(_ context.Context, _ string) ([]*domain.APIKey, error) {
	return nil, nil
}

func (s *TenantStore) RevokeAPIKey(_ context.Context, _, _ string) error {
	return nil
}

func (s *VirtualAccountStore) ListVAs(_ context.Context, _ string, _ int, _ string) ([]*domain.VirtualAccount, string, error) {
	return nil, "", nil
}

func (s *TransactionStore) ListTenantTransactions(_ context.Context, _ string, _ int, _ string) ([]*domain.Transaction, string, error) {
	return nil, "", nil
}

func (s *TransactionStore) GetTransactionForTenant(_ context.Context, _, _ string) (*domain.Transaction, error) {
	return nil, nil
}

func (s *RelayStore) GetDelivery(_ context.Context, id string) (*domain.RelayDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.deliveries {
		if d.ID == id {
			return d, nil
		}
	}
	return nil, nil
}

func (s *RelayStore) ListDeliveries(_ context.Context, tenantID string, limit int, _ string) ([]*domain.RelayDelivery, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*domain.RelayDelivery
	for _, d := range s.deliveries {
		ep, ok := s.endpoints[d.EndpointID]
		if !ok || ep.TenantID != tenantID {
			continue
		}
		out = append(out, d)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, "", nil
}
