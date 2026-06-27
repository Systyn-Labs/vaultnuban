// Package harness contains integration-style scenarios that exercise the full
// reconciliation pipeline without a real database or network.
//
// Each scenario uses fakeprov (in-memory Nomba provider) + memstore (in-memory
// repositories) so the tests are hermetic, fast, and runnable in CI with no
// external services.
//
// After every scenario the NFR-1 balance invariant is checked:
//
//	Σ credits across all ledger accounts == Σ debits across all ledger accounts
package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/systynlabs/vaultnuban/internal/config"
	"github.com/systynlabs/vaultnuban/internal/domain"
	"github.com/systynlabs/vaultnuban/internal/provider"
	"github.com/systynlabs/vaultnuban/internal/provider/fakeprov"
	"github.com/systynlabs/vaultnuban/internal/recon"
	"github.com/systynlabs/vaultnuban/internal/store/memstore"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// harness bundles everything a scenario needs.
type harness struct {
	ctx      context.Context
	fake     *fakeprov.Fake
	txnStore *memstore.TransactionStore
	vaStore  *memstore.VirtualAccountStore
	custStore *memstore.CustomerStore
	suspStore *memstore.SuspenseStore
	sweepStore *memstore.SweepStore
	worker   *recon.Worker
	sweep    *recon.SweepRunner
}

func newHarness(limits map[int]config.TierLimit) *harness {
	tierLimits := config.NewTierLimitsCache()
	if limits != nil {
		raw, _ := json.Marshal(func() map[string]config.TierLimit {
			m := make(map[string]config.TierLimit, len(limits))
			for k, v := range limits {
				m[fmt.Sprintf("%d", k)] = v
			}
			return m
		}())
		_ = tierLimits.Load(raw)
	}

	txnStore  := memstore.NewTransactionStore()
	vaStore   := memstore.NewVirtualAccountStore()
	custStore := memstore.NewCustomerStore()
	suspStore := memstore.NewSuspenseStore()
	sweepStore := memstore.NewSweepStore()
	whStore   := memstore.NewWebhookEventStore()
	fake      := fakeprov.New()

	matcher := recon.NewMatcher(vaStore, txnStore, tierLimits)
	worker  := recon.NewWorker(512, matcher, txnStore, whStore, suspStore, custStore)
	sweep   := recon.NewSweepRunner(fake, txnStore, sweepStore, worker, time.Hour, 5*time.Minute)

	return &harness{
		ctx:        context.Background(),
		fake:       fake,
		txnStore:   txnStore,
		vaStore:    vaStore,
		custStore:  custStore,
		suspStore:  suspStore,
		sweepStore: sweepStore,
		worker:     worker,
		sweep:      sweep,
	}
}

// assertBalanced verifies NFR-1: Σdebits == Σcredits across all ledger entries.
func assertBalanced(t *testing.T, txnStore *memstore.TransactionStore) {
	t.Helper()
	entries := txnStore.AllLedgerEntries()
	var credits, debits int64
	for _, e := range entries {
		if e.Direction == "credit" {
			credits += e.AmountKobo
		} else {
			debits += e.AmountKobo
		}
	}
	if credits != debits {
		t.Errorf("NFR-1 VIOLATED: Σcredits=%d Σdebits=%d (delta=%d kobo)", credits, debits, credits-debits)
	}
}

// seedActiveCustomer creates a tenant-scoped customer + active VA in memstore.
func (h *harness) seedActiveCustomer(t *testing.T, nuban, tenantID string, kycTier int) (customerID, vaID string) {
	t.Helper()

	bvn := "12345678901"
	cust, err := h.custStore.CreateCustomer(h.ctx, tenantID, "ext-"+nuban, "Test User",
		domain.IdentityInput{BVNMasked: &bvn, KYCTier: kycTier})
	if err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	va := &domain.VirtualAccount{
		CustomerID:      cust.ID,
		NUBAN:           nuban,
		NombaAccountRef: "ref-" + nuban,
		AccountName:     "Test User",
		Status:          domain.VAStatusActive,
	}
	if err := h.vaStore.CreateVirtualAccount(h.ctx, va); err != nil {
		t.Fatalf("seed VA: %v", err)
	}
	return cust.ID, va.ID
}

// processCreditDirect builds a payment_success WorkItem and calls ProcessDirect.
func (h *harness) processCreditDirect(t *testing.T, txnID, nuban string, amountKobo int64, at time.Time) recon.ProcessResult {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"id": txnID})
	item := recon.WorkItem{
		Payload: &provider.WebhookPayload{
			EventType: "payment_success",
			Transaction: provider.ProviderTransaction{
				TransactionID: txnID,
				AccountNumber: nuban,
				AmountKobo:    amountKobo,
				OccurredAt:    at,
				Status:        "success",
				Raw:           raw,
			},
			Raw: raw,
		},
		Source: "webhook",
	}
	res, err := h.worker.ProcessDirect(h.ctx, item)
	if err != nil {
		t.Fatalf("ProcessDirect(%s): %v", txnID, err)
	}
	return res
}

// processReversalDirect builds a payment_reversal WorkItem and calls ProcessDirect.
func (h *harness) processReversalDirect(t *testing.T, txnID, nuban string, amountKobo int64, at time.Time) recon.ProcessResult {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"id": txnID})
	item := recon.WorkItem{
		Payload: &provider.WebhookPayload{
			EventType: "payment_reversal",
			Transaction: provider.ProviderTransaction{
				TransactionID: txnID,
				AccountNumber: nuban,
				AmountKobo:    amountKobo,
				OccurredAt:    at,
				Status:        "reversed",
				Raw:           raw,
			},
			Raw: raw,
		},
		Source: "webhook",
	}
	res, err := h.worker.ProcessDirect(h.ctx, item)
	if err != nil {
		t.Fatalf("ProcessDirect reversal (%s): %v", txnID, err)
	}
	return res
}

// ── I-01: Webhook happy path ──────────────────────────────────────────────────

// TestI01_WebhookHappyPath verifies that a payment_success event for a known NUBAN
// credits the customer wallet, and the ledger remains balanced (NFR-1).
func TestI01_WebhookHappyPath(t *testing.T) {
	h := newHarness(nil)
	custID, _ := h.seedActiveCustomer(t, "1000000001", "tenant-1", 2)

	res := h.processCreditDirect(t, "txn-001", "1000000001", 500_000, time.Now())

	if !res.Posted {
		t.Fatalf("expected Posted=true, got %+v", res)
	}
	if res.Suspensed {
		t.Fatalf("expected Suspensed=false, got %+v", res)
	}

	balance, err := h.txnStore.GetBalance(h.ctx, domain.CustomerWalletAccount(custID))
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if balance != 500_000 {
		t.Errorf("expected balance=500000 kobo, got %d", balance)
	}

	assertBalanced(t, h.txnStore)
}

// ── I-02: Duplicate webhook (idempotency) ─────────────────────────────────────

// TestI02_DuplicateWebhookIdempotent verifies that reprocessing the same transactionID
// is a no-op — no duplicate ledger entries are posted.
func TestI02_DuplicateWebhookIdempotent(t *testing.T) {
	h := newHarness(nil)
	custID, _ := h.seedActiveCustomer(t, "1000000002", "tenant-1", 2)
	at := time.Now()

	res1 := h.processCreditDirect(t, "txn-002", "1000000002", 100_000, at)
	if !res1.Posted {
		t.Fatalf("first call: expected Posted=true")
	}

	res2 := h.processCreditDirect(t, "txn-002", "1000000002", 100_000, at)
	if !res2.Skipped {
		t.Fatalf("second call: expected Skipped=true, got %+v", res2)
	}

	balance, _ := h.txnStore.GetBalance(h.ctx, domain.CustomerWalletAccount(custID))
	if balance != 100_000 {
		t.Errorf("expected balance=100000 (no double-credit), got %d", balance)
	}

	assertBalanced(t, h.txnStore)
}

// ── I-03: Sweep recovery ──────────────────────────────────────────────────────

// TestI03_SweepRecovery verifies that a transaction seeded in the provider but absent
// from the ledger is picked up and posted by the sweep runner.
func TestI03_SweepRecovery(t *testing.T) {
	h := newHarness(nil)
	custID, _ := h.seedActiveCustomer(t, "1000000003", "tenant-1", 2)

	// Seed a transaction in the fake provider but NOT in the ledger (simulates missed webhook).
	missedAt := time.Now().Add(-30 * time.Minute)
	h.fake.SeedTransaction(provider.ProviderTransaction{
		TransactionID: "txn-003",
		AccountNumber: "1000000003",
		AmountKobo:    250_000,
		OccurredAt:    missedAt,
		Status:        "success",
	})

	// Verify it's not in the ledger yet.
	tx, _ := h.txnStore.GetTransaction(h.ctx, "txn-003")
	if tx != nil {
		t.Fatal("pre-condition: txn-003 should not exist yet")
	}

	// Run the sweep. The default interval is 1h, so the 30-minute-old tx is in-window.
	result, err := h.sweep.Run(h.ctx)
	if err != nil {
		t.Fatalf("sweep run: %v", err)
	}

	if result.Posted == 0 {
		t.Errorf("expected sweep to post at least 1 transaction, got %+v", result)
	}

	// Verify transaction is now in the ledger.
	tx, _ = h.txnStore.GetTransaction(h.ctx, "txn-003")
	if tx == nil {
		t.Fatal("expected txn-003 to be posted after sweep")
	}

	balance, _ := h.txnStore.GetBalance(h.ctx, domain.CustomerWalletAccount(custID))
	if balance != 250_000 {
		t.Errorf("expected balance=250000, got %d", balance)
	}

	assertBalanced(t, h.txnStore)
}

// ── I-04: Suspense — closed account ──────────────────────────────────────────

// TestI04_SuspenseClosedAccount verifies that a payment to a CLOSED virtual account
// is routed to the suspense ledger, not the customer wallet.
func TestI04_SuspenseClosedAccount(t *testing.T) {
	h := newHarness(nil)
	custID, vaID := h.seedActiveCustomer(t, "1000000004", "tenant-1", 2)

	// Close the VA.
	if err := h.vaStore.UpdateVAStatus(h.ctx, vaID, "CLOSED", "test"); err != nil {
		t.Fatalf("close VA: %v", err)
	}

	res := h.processCreditDirect(t, "txn-004", "1000000004", 300_000, time.Now())

	if !res.Suspensed {
		t.Fatalf("expected Suspensed=true for closed account, got %+v", res)
	}

	// Customer wallet must be empty.
	walBal, _ := h.txnStore.GetBalance(h.ctx, domain.CustomerWalletAccount(custID))
	if walBal != 0 {
		t.Errorf("expected customer wallet=0, got %d", walBal)
	}

	// Suspense account must hold the amount.
	suspBal, _ := h.txnStore.GetBalance(h.ctx, domain.AccountSuspense)
	if suspBal != 300_000 {
		t.Errorf("expected suspense balance=300000, got %d", suspBal)
	}

	// A suspense item must exist.
	items := h.suspStore.OpenItems()
	if len(items) != 1 {
		t.Fatalf("expected 1 open suspense item, got %d", len(items))
	}
	if items[0].Reason != domain.SuspenseReasonClosedAccount {
		t.Errorf("expected reason=%s, got %s", domain.SuspenseReasonClosedAccount, items[0].Reason)
	}

	assertBalanced(t, h.txnStore)
}

// ── I-05: Suspense — unmatched NUBAN ─────────────────────────────────────────

// TestI05_SuspenseUnmatched verifies that a payment to an unknown NUBAN
// lands in suspense with reason=unmatched.
func TestI05_SuspenseUnmatched(t *testing.T) {
	h := newHarness(nil)

	res := h.processCreditDirect(t, "txn-005", "9999999999", 150_000, time.Now())

	if !res.Suspensed {
		t.Fatalf("expected Suspensed=true for unknown NUBAN, got %+v", res)
	}

	items := h.suspStore.OpenItems()
	if len(items) != 1 {
		t.Fatalf("expected 1 open suspense item, got %d", len(items))
	}
	if items[0].Reason != domain.SuspenseReasonUnmatched {
		t.Errorf("expected reason=%s, got %s", domain.SuspenseReasonUnmatched, items[0].Reason)
	}

	suspBal, _ := h.txnStore.GetBalance(h.ctx, domain.AccountSuspense)
	if suspBal != 150_000 {
		t.Errorf("expected suspense=150000, got %d", suspBal)
	}

	assertBalanced(t, h.txnStore)
}

// ── I-06: Reversal of a previously credited payment ───────────────────────────

// TestI06_Reversal verifies that a payment_reversal correctly debits the customer
// wallet and the ledger remains balanced after the credit+reversal pair.
func TestI06_Reversal(t *testing.T) {
	h := newHarness(nil)
	custID, _ := h.seedActiveCustomer(t, "1000000006", "tenant-1", 2)
	at := time.Now()

	// First: credit.
	res1 := h.processCreditDirect(t, "txn-006-cr", "1000000006", 400_000, at)
	if !res1.Posted {
		t.Fatalf("credit: expected Posted=true")
	}

	balAfterCredit, _ := h.txnStore.GetBalance(h.ctx, domain.CustomerWalletAccount(custID))
	if balAfterCredit != 400_000 {
		t.Errorf("after credit: expected balance=400000, got %d", balAfterCredit)
	}

	// Then: reversal (different txn ID, same NUBAN).
	res2 := h.processReversalDirect(t, "txn-006-rv", "1000000006", 400_000, at.Add(time.Minute))
	if !res2.Posted {
		t.Fatalf("reversal: expected Posted=true, got %+v", res2)
	}

	balAfterReversal, _ := h.txnStore.GetBalance(h.ctx, domain.CustomerWalletAccount(custID))
	if balAfterReversal != 0 {
		t.Errorf("after reversal: expected balance=0, got %d", balAfterReversal)
	}

	assertBalanced(t, h.txnStore)
}

// ── I-07: Tier-limit cap → suspense ──────────────────────────────────────────

// TestI07_TierLimitSuspense verifies that a credit that would exceed the customer's
// KYC-tier max balance is routed to suspense instead.
func TestI07_TierLimitSuspense(t *testing.T) {
	tierLimits := map[int]config.TierLimit{
		1: {MaxBalanceKobo: 200_000, DailyCreditKobo: 500_000},
	}
	h := newHarness(tierLimits)
	custID, _ := h.seedActiveCustomer(t, "1000000007", "tenant-1", 1)

	// First credit: fits within limit.
	res1 := h.processCreditDirect(t, "txn-007a", "1000000007", 150_000, time.Now())
	if !res1.Posted || res1.Suspensed {
		t.Fatalf("first credit should post to wallet, got %+v", res1)
	}

	// Second credit: 150k already in wallet, 100k more = 250k > 200k limit → suspense.
	res2 := h.processCreditDirect(t, "txn-007b", "1000000007", 100_000, time.Now())
	if !res2.Suspensed {
		t.Fatalf("expected second credit to be suspensed due to tier limit, got %+v", res2)
	}

	walBal, _ := h.txnStore.GetBalance(h.ctx, domain.CustomerWalletAccount(custID))
	if walBal != 150_000 {
		t.Errorf("wallet balance should remain 150000, got %d", walBal)
	}

	suspBal, _ := h.txnStore.GetBalance(h.ctx, domain.AccountSuspense)
	if suspBal != 100_000 {
		t.Errorf("suspense should hold 100000, got %d", suspBal)
	}

	assertBalanced(t, h.txnStore)
}

// ── I-08: Sweep idempotency with pre-existing webhook ────────────────────────

// TestI08_SweepSkipsAlreadyPosted verifies that if a webhook already posted a
// transaction and then the sweep encounters the same txnID, it skips it.
func TestI08_SweepSkipsAlreadyPosted(t *testing.T) {
	h := newHarness(nil)
	h.seedActiveCustomer(t, "1000000008", "tenant-1", 2)

	txAt := time.Now().Add(-20 * time.Minute)

	// Simulate webhook posting it first.
	h.processCreditDirect(t, "txn-008", "1000000008", 50_000, txAt)

	// Also seed it in the provider so sweep finds it.
	h.fake.SeedTransaction(provider.ProviderTransaction{
		TransactionID: "txn-008",
		AccountNumber: "1000000008",
		AmountKobo:    50_000,
		OccurredAt:    txAt,
		Status:        "success",
	})

	result, err := h.sweep.Run(h.ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Skipped == 0 {
		t.Errorf("expected sweep to skip already-posted txn, got %+v", result)
	}
	if result.Posted > 0 {
		t.Errorf("expected sweep to post 0 new txns, got posted=%d", result.Posted)
	}

	assertBalanced(t, h.txnStore)
}
