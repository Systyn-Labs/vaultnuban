package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/systynlabs/vaultnuban/internal/domain"
)

type CustomerRepo struct{ pool *pgxpool.Pool }

func NewCustomerRepo(pool *pgxpool.Pool) *CustomerRepo { return &CustomerRepo{pool: pool} }

// CreateCustomer inserts a customer and its identity in a single transaction.
func (r *CustomerRepo) CreateCustomer(
	ctx context.Context,
	tenantID, externalRef, displayName string,
	identity domain.IdentityInput,
) (*domain.Customer, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("customer repo: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var c domain.Customer
	err = tx.QueryRow(ctx, `
		INSERT INTO customers(tenant_id, external_ref, display_name)
		VALUES($1, $2, $3)
		RETURNING id, tenant_id, external_ref, display_name, status, created_at, updated_at`,
		tenantID, externalRef, displayName,
	).Scan(&c.ID, &c.TenantID, &c.ExternalRef, &c.DisplayName, &c.Status, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("customer repo: insert customer: %w", err)
	}

	id := domain.Identity{CustomerID: c.ID, KYCTier: identity.KYCTier, VerificationStatus: "pending"}
	if identity.BVNMasked != nil {
		id.BVNMasked = identity.BVNMasked
	}
	if identity.NINMasked != nil {
		id.NINMasked = identity.NINMasked
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO identities(customer_id, bvn_masked, nin_masked, kyc_tier, verification_status)
		VALUES($1, $2, $3, $4, $5)
		RETURNING id, customer_id, bvn_masked, nin_masked, kyc_tier, verification_status, created_at, updated_at`,
		c.ID, id.BVNMasked, id.NINMasked, id.KYCTier, id.VerificationStatus,
	).Scan(&id.ID, &id.CustomerID, &id.BVNMasked, &id.NINMasked, &id.KYCTier, &id.VerificationStatus, &id.CreatedAt, &id.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("customer repo: insert identity: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("customer repo: commit: %w", err)
	}

	c.Identity = &id
	return &c, nil
}

func (r *CustomerRepo) GetCustomer(ctx context.Context, tenantID, customerID string) (*domain.Customer, error) {
	c, err := r.scanCustomer(ctx, `
		SELECT c.id, c.tenant_id, c.external_ref, c.display_name, c.status, c.created_at, c.updated_at,
		       i.id, i.customer_id, i.bvn_masked, i.nin_masked, i.kyc_tier, i.verification_status, i.created_at, i.updated_at
		FROM customers c
		LEFT JOIN identities i ON i.customer_id = c.id
		WHERE c.id = $1 AND c.tenant_id = $2`,
		customerID, tenantID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func (r *CustomerRepo) GetCustomerByExternalRef(ctx context.Context, tenantID, externalRef string) (*domain.Customer, error) {
	c, err := r.scanCustomer(ctx, `
		SELECT c.id, c.tenant_id, c.external_ref, c.display_name, c.status, c.created_at, c.updated_at,
		       i.id, i.customer_id, i.bvn_masked, i.nin_masked, i.kyc_tier, i.verification_status, i.created_at, i.updated_at
		FROM customers c
		LEFT JOIN identities i ON i.customer_id = c.id
		WHERE c.external_ref = $1 AND c.tenant_id = $2`,
		externalRef, tenantID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func (r *CustomerRepo) UpdateKYCTier(ctx context.Context, customerID string, newTier int, actor string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE identities SET kyc_tier = $1, updated_at = NOW() WHERE customer_id = $2`,
		newTier, customerID,
	)
	if err != nil {
		return fmt.Errorf("customer repo: update kyc tier: %w", err)
	}
	return nil
}

func (r *CustomerRepo) ListCustomers(ctx context.Context, tenantID string, limit int, cursor string) ([]*domain.Customer, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	// cursor is the ID of the last item seen; use created_at of that row for keyset pagination
	var query string
	var args []any
	if cursor == "" {
		query = `
			SELECT c.id, c.tenant_id, c.external_ref, c.display_name, c.status, c.created_at, c.updated_at,
			       COALESCE(i.id::text,''), COALESCE(i.customer_id::text,''), i.bvn_masked, i.nin_masked,
			       COALESCE(i.kyc_tier,0), COALESCE(i.verification_status,''), COALESCE(i.created_at,NOW()), COALESCE(i.updated_at,NOW())
			FROM customers c
			LEFT JOIN identities i ON i.customer_id = c.id
			WHERE c.tenant_id = $1
			ORDER BY c.created_at DESC, c.id DESC
			LIMIT $2`
		args = []any{tenantID, limit + 1}
	} else {
		query = `
			SELECT c.id, c.tenant_id, c.external_ref, c.display_name, c.status, c.created_at, c.updated_at,
			       COALESCE(i.id::text,''), COALESCE(i.customer_id::text,''), i.bvn_masked, i.nin_masked,
			       COALESCE(i.kyc_tier,0), COALESCE(i.verification_status,''), COALESCE(i.created_at,NOW()), COALESCE(i.updated_at,NOW())
			FROM customers c
			LEFT JOIN identities i ON i.customer_id = c.id
			WHERE c.tenant_id = $1
			  AND (c.created_at, c.id) < (SELECT created_at, id FROM customers WHERE id = $3)
			ORDER BY c.created_at DESC, c.id DESC
			LIMIT $2`
		args = []any{tenantID, limit + 1, cursor}
	}

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("customer repo: list: %w", err)
	}
	defer rows.Close()

	var out []*domain.Customer
	for rows.Next() {
		var c domain.Customer
		var id domain.Identity
		if err := rows.Scan(
			&c.ID, &c.TenantID, &c.ExternalRef, &c.DisplayName, &c.Status, &c.CreatedAt, &c.UpdatedAt,
			&id.ID, &id.CustomerID, &id.BVNMasked, &id.NINMasked, &id.KYCTier, &id.VerificationStatus, &id.CreatedAt, &id.UpdatedAt,
		); err != nil {
			return nil, "", fmt.Errorf("customer repo: list scan: %w", err)
		}
		c.Identity = &id
		out = append(out, &c)
	}
	if rows.Err() != nil {
		return nil, "", fmt.Errorf("customer repo: list iter: %w", rows.Err())
	}

	var nextCursor string
	if len(out) > limit {
		nextCursor = out[limit-1].ID
		out = out[:limit]
	}
	return out, nextCursor, nil
}

func (r *CustomerRepo) scanCustomer(ctx context.Context, query string, args ...any) (*domain.Customer, error) {
	var c domain.Customer
	var id domain.Identity
	err := r.pool.QueryRow(ctx, query, args...).Scan(
		&c.ID, &c.TenantID, &c.ExternalRef, &c.DisplayName, &c.Status, &c.CreatedAt, &c.UpdatedAt,
		&id.ID, &id.CustomerID, &id.BVNMasked, &id.NINMasked, &id.KYCTier, &id.VerificationStatus, &id.CreatedAt, &id.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	c.Identity = &id
	return &c, nil
}
