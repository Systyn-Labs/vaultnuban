package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/systynlabs/vaultnuban/internal/domain"
	"github.com/systynlabs/vaultnuban/internal/provider"
	"github.com/systynlabs/vaultnuban/internal/store"
)

// ProvisioningService handles virtual account lifecycle operations.
type ProvisioningService struct {
	customers store.CustomerStore
	accounts  store.VirtualAccountStore
	audit     store.AuditStore
	prov      provider.Provider
}

func NewProvisioningService(
	customers store.CustomerStore,
	accounts store.VirtualAccountStore,
	audit store.AuditStore,
	prov provider.Provider,
) *ProvisioningService {
	return &ProvisioningService{
		customers: customers,
		accounts:  accounts,
		audit:     audit,
		prov:      prov,
	}
}

// ProvisionVA provisions a static virtual account for a customer.
// If one already exists, it returns it with created=false (FR-3.3).
func (s *ProvisioningService) ProvisionVA(
	ctx context.Context,
	tenantID, customerID, actor string,
) (*domain.VirtualAccount, bool, error) {
	customer, err := s.customers.GetCustomer(ctx, tenantID, customerID)
	if err != nil {
		return nil, false, err
	}
	if customer == nil {
		return nil, false, nil // caller maps to 404
	}

	// FR-3.3: check for existing active VA before calling Nomba.
	existing, err := s.accounts.GetActiveVA(ctx, customerID)
	if err != nil {
		return nil, false, fmt.Errorf("provisioning: get active VA: %w", err)
	}
	if existing != nil {
		return existing, false, nil
	}

	accountRef := buildAccountRef(tenantID, customerID)
	accountName := buildAccountName(customer)

	vaResp, err := s.prov.CreateVA(ctx, provider.CreateVARequest{
		AccountRef:  accountRef,
		AccountName: accountName,
	})
	if err != nil {
		return nil, false, fmt.Errorf("provisioning: provider create VA: %w", err)
	}

	va := &domain.VirtualAccount{
		CustomerID:      customerID,
		NombaAccountRef: vaResp.AccountRef,
		NUBAN:           vaResp.NUBAN,
		BankName:        vaResp.BankName,
		AccountName:     vaResp.AccountName,
		NombaHolderID:   vaResp.AccountHolderID,
		Status:          domain.VAStatusActive,
	}
	if err := s.accounts.CreateVirtualAccount(ctx, va); err != nil {
		return nil, false, fmt.Errorf("provisioning: persist VA: %w", err)
	}

	after, _ := json.Marshal(va)
	_ = s.audit.Append(ctx, &domain.AuditEntry{
		TenantID:    &tenantID,
		Actor:       actor,
		Action:      "provision_va",
		EntityType:  "virtual_account",
		EntityID:    va.ID,
		BeforeAfter: after,
	})

	return va, true, nil
}

// GetVA returns the active VA for a customer scoped to the tenant.
func (s *ProvisioningService) GetVA(ctx context.Context, tenantID, customerID string) (*domain.VirtualAccount, error) {
	customer, err := s.customers.GetCustomer(ctx, tenantID, customerID)
	if err != nil || customer == nil {
		return nil, err
	}
	return s.accounts.GetActiveVA(ctx, customerID)
}

// RenameVA changes the account name on Nomba and locally. NUBAN is unchanged (FR-8.1).
func (s *ProvisioningService) RenameVA(
	ctx context.Context,
	tenantID, customerID, newName, actor string,
) (*domain.VirtualAccount, error) {
	va, err := s.mustGetActiveVA(ctx, tenantID, customerID)
	if err != nil || va == nil {
		return va, err
	}

	oldName := va.AccountName

	if err := s.prov.UpdateVA(ctx, va.NombaAccountRef, newName); err != nil {
		return nil, fmt.Errorf("provisioning: provider update VA: %w", err)
	}
	if err := s.accounts.RenameVA(ctx, va.ID, newName, actor); err != nil {
		return nil, fmt.Errorf("provisioning: rename VA locally: %w", err)
	}

	va.AccountName = newName
	_ = s.audit.Append(ctx, &domain.AuditEntry{
		TenantID:    &tenantID,
		Actor:       actor,
		Action:      "rename_va",
		EntityType:  "virtual_account",
		EntityID:    va.ID,
		BeforeAfter: mustMarshalBeforeAfter(map[string]any{"account_name": oldName}, map[string]any{"account_name": newName}),
	})

	return va, nil
}

// CloseVA transitions the VA to CLOSED (terminal). Post-closure credits go to suspense (FR-8.2).
func (s *ProvisioningService) CloseVA(
	ctx context.Context,
	tenantID, customerID, actor string,
) error {
	va, err := s.mustGetActiveVA(ctx, tenantID, customerID)
	if err != nil || va == nil {
		return err
	}

	if !va.Status.ValidTransition(domain.VAStatusClosed) {
		return &StateError{Current: string(va.Status), Target: string(domain.VAStatusClosed)}
	}

	// Best-effort Nomba call; sandbox may not support expiry but local state enforces closure.
	_ = s.prov.CloseVA(ctx, va.NombaAccountRef)

	if err := s.accounts.UpdateVAStatus(ctx, va.ID, string(domain.VAStatusClosed), actor); err != nil {
		return fmt.Errorf("provisioning: close VA: %w", err)
	}

	before, _ := json.Marshal(map[string]any{"status": string(va.Status)})
	after, _ := json.Marshal(map[string]any{"status": string(domain.VAStatusClosed)})
	_ = s.audit.Append(ctx, &domain.AuditEntry{
		TenantID:    &tenantID,
		Actor:       actor,
		Action:      "close_va",
		EntityType:  "virtual_account",
		EntityID:    va.ID,
		BeforeAfter: mustMarshalBeforeAfter(json.RawMessage(before), json.RawMessage(after)),
	})

	return nil
}

// SuspendVA transitions the VA to SUSPENDED (reversible).
func (s *ProvisioningService) SuspendVA(
	ctx context.Context,
	tenantID, customerID, actor string,
) error {
	va, err := s.mustGetActiveVA(ctx, tenantID, customerID)
	if err != nil || va == nil {
		return err
	}
	if !va.Status.ValidTransition(domain.VAStatusSuspended) {
		return &StateError{Current: string(va.Status), Target: string(domain.VAStatusSuspended)}
	}

	_ = s.prov.SuspendVA(ctx, va.NombaHolderID)

	if err := s.accounts.UpdateVAStatus(ctx, va.ID, string(domain.VAStatusSuspended), actor); err != nil {
		return fmt.Errorf("provisioning: suspend VA: %w", err)
	}

	_ = s.audit.Append(ctx, &domain.AuditEntry{
		TenantID:   &tenantID,
		Actor:      actor,
		Action:     "suspend_va",
		EntityType: "virtual_account",
		EntityID:   va.ID,
	})
	return nil
}

// UnsuspendVA transitions a SUSPENDED VA back to ACTIVE.
func (s *ProvisioningService) UnsuspendVA(
	ctx context.Context,
	tenantID, customerID, actor string,
) error {
	customer, err := s.customers.GetCustomer(ctx, tenantID, customerID)
	if err != nil || customer == nil {
		return err
	}

	// We need the suspended VA specifically (not just ACTIVE).
	va, err := s.accounts.GetVAByCustomerAndStatus(ctx, customerID, string(domain.VAStatusSuspended))
	if err != nil || va == nil {
		return err
	}
	if !va.Status.ValidTransition(domain.VAStatusActive) {
		return &StateError{Current: string(va.Status), Target: string(domain.VAStatusActive)}
	}

	if err := s.accounts.UpdateVAStatus(ctx, va.ID, string(domain.VAStatusActive), actor); err != nil {
		return fmt.Errorf("provisioning: unsuspend VA: %w", err)
	}

	_ = s.audit.Append(ctx, &domain.AuditEntry{
		TenantID:   &tenantID,
		Actor:      actor,
		Action:     "unsuspend_va",
		EntityType: "virtual_account",
		EntityID:   va.ID,
	})
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (s *ProvisioningService) mustGetActiveVA(ctx context.Context, tenantID, customerID string) (*domain.VirtualAccount, error) {
	customer, err := s.customers.GetCustomer(ctx, tenantID, customerID)
	if err != nil {
		return nil, err
	}
	if customer == nil {
		return nil, nil // caller maps to 404
	}
	va, err := s.accounts.GetActiveVA(ctx, customerID)
	if err != nil {
		return nil, err
	}
	return va, nil
}

// buildAccountRef produces a deterministic, unique accountRef for Nomba (FR-3.1).
// Format: t{8 hex from tenantID}c{32 hex from customerID} = 42 chars.
func buildAccountRef(tenantID, customerID string) string {
	t := strings.ReplaceAll(tenantID, "-", "")
	c := strings.ReplaceAll(customerID, "-", "")
	if len(t) > 8 {
		t = t[:8]
	}
	return "t" + t + "c" + c
}

// buildAccountName derives the Nomba account name from the customer's identity.
// Nomba requires 8–64 chars; we use the display_name, padded/truncated as needed.
func buildAccountName(c *domain.Customer) string {
	name := c.DisplayName
	if len(name) < 8 {
		name = name + strings.Repeat(" ", 8-len(name))
	}
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

// StateError is returned for illegal VA state transitions (FR-8.5).
type StateError struct {
	Current string
	Target  string
}

func (e *StateError) Error() string {
	return fmt.Sprintf("invalid state transition: %s → %s", e.Current, e.Target)
}
