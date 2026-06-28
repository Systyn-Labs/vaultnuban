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

func (r *TenantRepo) CreateAPIKey(ctx context.Context, tenantID, rawKey, keyHash, keyPrefix string) (*domain.APIKey, error) {
	var k domain.APIKey
	err := r.pool.QueryRow(ctx,
		`INSERT INTO api_keys(tenant_id, raw_key, key_hash, key_prefix) VALUES($1,$2,$3,$4)
		 RETURNING id, tenant_id, COALESCE(raw_key,''), key_hash, key_prefix, active`,
		tenantID, rawKey, keyHash, keyPrefix,
	).Scan(&k.ID, &k.TenantID, &k.RawKey, &k.KeyHash, &k.KeyPrefix, &k.Active)
	if err != nil {
		return nil, fmt.Errorf("tenant repo: create api key: %w", err)
	}
	return &k, nil
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

func (r *TenantRepo) ListAPIKeys(ctx context.Context, tenantID string) ([]*domain.APIKey, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, key_prefix, active, created_at
		FROM api_keys
		WHERE tenant_id = $1 AND active = TRUE
		ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("tenant repo: list api keys: %w", err)
	}
	defer rows.Close()
	var out []*domain.APIKey
	for rows.Next() {
		var k domain.APIKey
		if err := rows.Scan(&k.ID, &k.TenantID, &k.KeyPrefix, &k.Active, &k.CreatedAt); err != nil {
			return nil, fmt.Errorf("tenant repo: list api keys scan: %w", err)
		}
		out = append(out, &k)
	}
	return out, nil
}

func (r *TenantRepo) RevokeAPIKey(ctx context.Context, keyID, tenantID string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE api_keys SET active = FALSE WHERE id = $1 AND tenant_id = $2`, keyID, tenantID)
	if err != nil {
		return fmt.Errorf("tenant repo: revoke api key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("tenant repo: api key not found or not owned by tenant")
	}
	return nil
}

func (r *TenantRepo) ListTenants(ctx context.Context) ([]*domain.Tenant, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, name, created_at FROM tenants ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("tenant repo: list: %w", err)
	}
	defer rows.Close()
	var tenants []*domain.Tenant
	for rows.Next() {
		var t domain.Tenant
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("tenant repo: list scan: %w", err)
		}
		tenants = append(tenants, &t)
	}
	return tenants, nil
}
