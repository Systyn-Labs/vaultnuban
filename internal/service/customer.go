// Package service contains business logic that orchestrates stores and the provider.
package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/systynlabs/vaultnuban/internal/domain"
	"github.com/systynlabs/vaultnuban/internal/store"
)

// CustomerService handles customer and identity operations.
type CustomerService struct {
	customers store.CustomerStore
	audit     store.AuditStore
}

func NewCustomerService(customers store.CustomerStore, audit store.AuditStore) *CustomerService {
	return &CustomerService{customers: customers, audit: audit}
}

// CreateCustomer validates the identity input, creates the customer record, and
// writes an audit entry. Returns the created customer (FR-2.1, FR-2.3).
func (s *CustomerService) CreateCustomer(
	ctx context.Context,
	tenantID, externalRef, displayName string,
	identity domain.IdentityInput,
	actor string,
) (*domain.Customer, error) {
	if err := identity.Validate(); err != nil {
		return nil, &ValidationError{Field: "identity", Detail: err.Error()}
	}

	// Idempotent: if a customer with this external_ref already exists, return it.
	existing, err := s.customers.GetCustomerByExternalRef(ctx, tenantID, externalRef)
	if err != nil {
		return nil, fmt.Errorf("customer service: check existing: %w", err)
	}
	if existing != nil {
		return existing, nil
	}

	customer, err := s.customers.CreateCustomer(ctx, tenantID, externalRef, displayName, identity)
	if err != nil {
		return nil, fmt.Errorf("customer service: create: %w", err)
	}

	after, _ := json.Marshal(customer)
	_ = s.audit.Append(ctx, &domain.AuditEntry{
		TenantID:    &tenantID,
		Actor:       actor,
		Action:      "create_customer",
		EntityType:  "customer",
		EntityID:    customer.ID,
		BeforeAfter: after,
	})

	return customer, nil
}

// GetCustomer fetches a customer scoped to the tenant. Returns nil if not found
// (caller must map to 404).
func (s *CustomerService) GetCustomer(ctx context.Context, tenantID, customerID string) (*domain.Customer, error) {
	return s.customers.GetCustomer(ctx, tenantID, customerID)
}

// UpdateKYCTier changes the customer's KYC tier and writes an audit row (FR-8.4).
func (s *CustomerService) UpdateKYCTier(
	ctx context.Context,
	tenantID, customerID string,
	newTier int,
	actor string,
) (*domain.Customer, error) {
	if newTier < 1 || newTier > 3 {
		return nil, &ValidationError{Field: "kyc_tier", Detail: "must be 1, 2, or 3"}
	}

	customer, err := s.customers.GetCustomer(ctx, tenantID, customerID)
	if err != nil {
		return nil, err
	}
	if customer == nil {
		return nil, nil
	}

	before, _ := json.Marshal(customer.Identity)

	if err := s.customers.UpdateKYCTier(ctx, customerID, newTier, actor); err != nil {
		return nil, fmt.Errorf("customer service: update tier: %w", err)
	}

	oldTier := customer.Identity.KYCTier
	customer.Identity.KYCTier = newTier

	after, _ := json.Marshal(customer.Identity)
	_ = s.audit.Append(ctx, &domain.AuditEntry{
		TenantID:    &tenantID,
		Actor:       actor,
		Action:      "kyc_tier_change",
		EntityType:  "identity",
		EntityID:    customer.Identity.ID,
		BeforeAfter: mustMarshalBeforeAfter(map[string]any{"tier": oldTier}, map[string]any{"tier": newTier, "full_before": json.RawMessage(before), "full_after": json.RawMessage(after)}),
	})

	return customer, nil
}

// ValidationError is returned when request data fails domain validation.
type ValidationError struct {
	Field  string
	Detail string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation error on %s: %s", e.Field, e.Detail)
}

func mustMarshalBeforeAfter(before, after any) []byte {
	b, _ := json.Marshal(map[string]any{"before": before, "after": after})
	return b
}
