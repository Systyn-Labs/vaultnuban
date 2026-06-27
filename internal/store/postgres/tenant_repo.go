package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/systynlabs/vaultnuban/internal/domain"
)

type TenantRepo struct{ pool *pgxpool.Pool }

func NewTenantRepo(pool *pgxpool.Pool) *TenantRepo { return &TenantRepo{pool: pool} }

func (r *TenantRepo) CreateTenant(ctx context.Context, name string) (*domain.Tenant, error) {
	var t domain.Tenant
	err := r.pool.QueryRow(ctx,
		`INSERT INTO tenants(name) VALUES($1) RETURNING id, name, created_at`,
		name,
	).Scan(&t.ID, &t.Name, &t.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("tenant repo: create: %w", err)
	}
	return &t, nil
}

// GetTenantByAPIKey looks up an active API key by its SHA-256 hash and returns
// the owning tenant. Returns nil, nil, nil when not found (caller maps to 401).
func (r *TenantRepo) GetTenantByAPIKey(ctx context.Context, keyHash string) (*domain.Tenant, *domain.APIKey, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT t.id, t.name, t.created_at,
		       k.id, k.tenant_id, k.key_hash, k.key_prefix, k.active
		FROM api_keys k
		JOIN tenants t ON t.id = k.tenant_id
		WHERE k.key_hash = $1 AND k.active = TRUE`,
		keyHash,
	)

	var t domain.Tenant
	var k domain.APIKey
	err := row.Scan(
		&t.ID, &t.Name, &t.CreatedAt,
		&k.ID, &k.TenantID, &k.KeyHash, &k.KeyPrefix, &k.Active,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("tenant repo: get by api key: %w", err)
	}
	return &t, &k, nil
}
