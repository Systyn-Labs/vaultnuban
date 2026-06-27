package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/systynlabs/vaultnuban/internal/domain"
	"github.com/systynlabs/vaultnuban/internal/ledger"
	"github.com/systynlabs/vaultnuban/internal/store"
)

// SuspenseService handles suspense listing and resolution (FR-7.2).
type SuspenseService struct {
	suspense  store.SuspenseStore
	txns      store.TransactionStore
	customers store.CustomerStore
	accounts  store.VirtualAccountStore
	audit     store.AuditStore
}

func NewSuspenseService(
	suspense store.SuspenseStore,
	txns store.TransactionStore,
	customers store.CustomerStore,
	accounts store.VirtualAccountStore,
	audit store.AuditStore,
) *SuspenseService {
	return &SuspenseService{
		suspense:  suspense,
		txns:      txns,
		customers: customers,
		accounts:  accounts,
		audit:     audit,
	}
}

func (s *SuspenseService) ListItems(
	ctx context.Context,
	tenantID string,
	limit int,
	cursor string,
) ([]*domain.SuspenseItem, string, error) {
	return s.suspense.ListSuspenseItems(ctx, tenantID, limit, cursor)
}

func (s *SuspenseService) GetItem(ctx context.Context, itemID string) (*domain.SuspenseItem, error) {
	return s.suspense.GetSuspenseItem(ctx, itemID)
}

// ResolveRequest carries the resolution parameters.
type ResolveRequest struct {
	Resolution       string // "reassign" | "refund_flagged"
	TargetCustomerID string // required for "reassign"
	Notes            string
	Actor            string
	TenantID         string
}

// Resolve executes a suspense resolution (FR-7.2).
//
// reassign:      DR suspense / CR customer_wallet + update status + audit
// refund_flagged: update status to refund_flagged + audit (funds stay in suspense)
func (s *SuspenseService) Resolve(ctx context.Context, itemID string, req ResolveRequest) error {
	item, err := s.suspense.GetSuspenseItem(ctx, itemID)
	if err != nil {
		return fmt.Errorf("suspense: get item: %w", err)
	}
	if item == nil {
		return &NotFoundError{Entity: "suspense_item", ID: itemID}
	}
	if item.Status != "open" {
		return &ValidationError{Field: "status", Detail: "suspense item is already resolved"}
	}

	switch req.Resolution {
	case "reassign":
		return s.resolveByReassign(ctx, item, req)
	case "refund_flagged":
		return s.resolveByRefundFlag(ctx, item, req)
	default:
		return &ValidationError{Field: "resolution", Detail: "must be 'reassign' or 'refund_flagged'"}
	}
}

func (s *SuspenseService) resolveByReassign(ctx context.Context, item *domain.SuspenseItem, req ResolveRequest) error {
	if req.TargetCustomerID == "" {
		return &ValidationError{Field: "target_customer_id", Detail: "required for reassign"}
	}

	// Validate target customer belongs to this tenant.
	customer, err := s.customers.GetCustomer(ctx, req.TenantID, req.TargetCustomerID)
	if err != nil {
		return fmt.Errorf("suspense: get target customer: %w", err)
	}
	if customer == nil {
		return &NotFoundError{Entity: "customer", ID: req.TargetCustomerID}
	}

	// Fetch original transaction to get the amount.
	origTx, err := s.txns.GetTransaction(ctx, item.TransactionID)
	if err != nil || origTx == nil {
		return fmt.Errorf("suspense: get original transaction: %w", err)
	}

	// Compensating entries: DR suspense → CR customer_wallet (FR-7.2, ledger.SuspenseToCustomer).
	entries, err := ledger.SuspenseToCustomer(
		resolutionTxID(itemID(item)),
		req.TargetCustomerID,
		origTx.AmountKobo,
	)
	if err != nil {
		return err
	}

	// Look up target VA for the resolution transaction record (may be nil).
	va, _ := s.accounts.GetActiveVA(ctx, req.TargetCustomerID)
	var vaID *string
	if va != nil {
		id := va.ID
		vaID = &id
	}

	resolutionID := resolutionTxID(itemID(item))
	resTx := &domain.Transaction{
		ID:               resolutionID,
		VirtualAccountID: vaID,
		AmountKobo:       origTx.AmountKobo,
		Direction:        "credit",
		Source:           "internal",
		Status:           "posted",
		Narration:        strPtr("suspense resolution: reassign to customer " + req.TargetCustomerID),
		Raw:              mustMarshal(map[string]string{"type": "suspense_resolution", "suspense_item_id": item.ID}),
		OccurredAt:       time.Now().UTC(),
	}

	if _, err := s.txns.PostTransaction(ctx, resTx, entries); err != nil {
		return fmt.Errorf("suspense: post resolution entries: %w", err)
	}

	if err := s.suspense.ResolveSuspenseItem(ctx, item.ID, "reassigned", req.Actor, req.Notes); err != nil {
		return fmt.Errorf("suspense: update status: %w", err)
	}

	_ = s.audit.Append(ctx, &domain.AuditEntry{
		TenantID:   &req.TenantID,
		Actor:      req.Actor,
		Action:     "suspense_resolve",
		EntityType: "suspense_item",
		EntityID:   item.ID,
		BeforeAfter: mustMarshalBA(
			map[string]any{"status": "open"},
			map[string]any{"status": "reassigned", "target_customer": req.TargetCustomerID, "notes": req.Notes},
		),
	})
	return nil
}

func (s *SuspenseService) resolveByRefundFlag(ctx context.Context, item *domain.SuspenseItem, req ResolveRequest) error {
	// Funds stay in the suspense ledger account — only the status changes (FR-7.2).
	if err := s.suspense.ResolveSuspenseItem(ctx, item.ID, "refund_flagged", req.Actor, req.Notes); err != nil {
		return fmt.Errorf("suspense: update status: %w", err)
	}

	_ = s.audit.Append(ctx, &domain.AuditEntry{
		TenantID:   &req.TenantID,
		Actor:      req.Actor,
		Action:     "suspense_resolve",
		EntityType: "suspense_item",
		EntityID:   item.ID,
		BeforeAfter: mustMarshalBA(
			map[string]any{"status": "open"},
			map[string]any{"status": "refund_flagged", "notes": req.Notes},
		),
	})
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// resolutionTxID builds a deterministic internal transaction ID for a resolution.
// Prefixed "vn:" to distinguish from Nomba transactionIds.
func resolutionTxID(itemID string) string { return "vn:res:" + itemID }

func itemID(item *domain.SuspenseItem) string { return item.ID }

func strPtr(s string) *string { return &s }

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func mustMarshalBA(before, after any) []byte {
	b, _ := json.Marshal(map[string]any{"before": before, "after": after})
	return b
}

// NotFoundError indicates a resource does not exist (handler maps to 404).
type NotFoundError struct {
	Entity string
	ID     string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("%s not found: %s", e.Entity, e.ID)
}
