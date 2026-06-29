package service

import (
	"context"
	"fmt"
	"time"

	"github.com/systynlabs/vaultnuban/internal/domain"
	"github.com/systynlabs/vaultnuban/internal/store"
)

// CollectionService manages dynamic payment collection records.
type CollectionService struct {
	collections store.CollectionStore
	customers   store.CustomerStore
	accounts    store.VirtualAccountStore
}

func NewCollectionService(
	collections store.CollectionStore,
	customers store.CustomerStore,
	accounts store.VirtualAccountStore,
) *CollectionService {
	return &CollectionService{
		collections: collections,
		customers:   customers,
		accounts:    accounts,
	}
}

type CreateCollectionRequest struct {
	CustomerID          string
	TenantID            string
	ExpectedAmountKobo  *int64
	Reference           string
	Description         string
	ExpiresInSeconds    int // 0 = no expiry
}

func (s *CollectionService) Create(ctx context.Context, req CreateCollectionRequest) (*domain.Collection, error) {
	if req.Reference == "" {
		return nil, &ValidationError{Field: "reference", Detail: "required"}
	}

	// Validate customer belongs to tenant.
	customer, err := s.customers.GetCustomer(ctx, req.TenantID, req.CustomerID)
	if err != nil || customer == nil {
		return nil, &NotFoundError{Entity: "customer", ID: req.CustomerID}
	}

	// Look up customer's active VA.
	va, err := s.accounts.GetActiveVA(ctx, req.CustomerID)
	if err != nil {
		return nil, fmt.Errorf("collection: get VA: %w", err)
	}
	if va == nil {
		return nil, &ValidationError{Field: "customer_id", Detail: "customer has no active virtual account"}
	}

	vaID := va.ID
	c := &domain.Collection{
		CustomerID:       req.CustomerID,
		VirtualAccountID: &vaID,
		Reference:        req.Reference,
		Description:      req.Description,
		Status:           "open",
	}
	if req.ExpectedAmountKobo != nil && *req.ExpectedAmountKobo > 0 {
		c.ExpectedAmountKobo = req.ExpectedAmountKobo
	}
	if req.ExpiresInSeconds > 0 {
		t := time.Now().Add(time.Duration(req.ExpiresInSeconds) * time.Second)
		c.ExpiresAt = &t
	}

	if err := s.collections.CreateCollection(ctx, c); err != nil {
		return nil, fmt.Errorf("collection: create: %w", err)
	}

	// Enrich with VA details for the response.
	c.NUBAN = va.NUBAN
	c.BankName = va.BankName
	return c, nil
}

func (s *CollectionService) Get(ctx context.Context, collectionID string) (*domain.Collection, error) {
	c, err := s.collections.GetCollection(ctx, collectionID)
	if err != nil {
		return nil, fmt.Errorf("collection: get: %w", err)
	}
	if c == nil {
		return nil, &NotFoundError{Entity: "collection", ID: collectionID}
	}
	return c, nil
}

func (s *CollectionService) List(ctx context.Context, customerID string, limit int, cursor string) ([]*domain.Collection, string, error) {
	return s.collections.ListCollections(ctx, customerID, limit, cursor)
}

func (s *CollectionService) Cancel(ctx context.Context, collectionID, tenantID string) error {
	c, err := s.collections.GetCollection(ctx, collectionID)
	if err != nil || c == nil {
		return &NotFoundError{Entity: "collection", ID: collectionID}
	}
	if c.Status != "open" {
		return &ValidationError{Field: "status", Detail: "collection is not open"}
	}
	// Verify customer belongs to tenant.
	customer, err := s.customers.GetCustomerByID(ctx, c.CustomerID)
	if err != nil || customer == nil || customer.TenantID != tenantID {
		return &NotFoundError{Entity: "collection", ID: collectionID}
	}
	return s.collections.CancelCollection(ctx, collectionID)
}

// TryFulfill checks if an incoming transaction fulfills an open collection for the VA.
// Called by the recon worker after successfully posting a transaction.
func (s *CollectionService) TryFulfill(ctx context.Context, vaID, txnID string, amountKobo int64) error {
	c, err := s.collections.FindOpenCollectionForVA(ctx, vaID, amountKobo)
	if err != nil {
		return fmt.Errorf("collection: find open: %w", err)
	}
	if c == nil {
		return nil // no matching collection — normal payment
	}
	return s.collections.FulfillCollection(ctx, c.ID, txnID)
}
