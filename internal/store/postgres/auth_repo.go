package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/systynlabs/vaultnuban/internal/domain"
)

type AuthRepo struct{ pool *pgxpool.Pool }

func NewAuthRepo(pool *pgxpool.Pool) *AuthRepo { return &AuthRepo{pool: pool} }

func (r *AuthRepo) CreateCredential(ctx context.Context, cred *domain.UserCredential) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO user_credentials(tenant_id, email, password_hash, name, role)
		 VALUES($1,$2,$3,$4,$5)`,
		cred.TenantID, cred.Email, cred.PasswordHash, cred.Name, cred.Role,
	)
	if err != nil {
		return fmt.Errorf("auth repo: create credential: %w", err)
	}
	return nil
}

// GetCredentialByEmail returns the credential, its tenant (nil for admin), and the
// tenant's active API key (nil for admin). Returns nil credential (no error) when not found.
func (r *AuthRepo) GetCredentialByEmail(ctx context.Context, email string) (*domain.UserCredential, *domain.Tenant, *domain.APIKey, error) {
	var cred domain.UserCredential
	err := r.pool.QueryRow(ctx,
		`SELECT id, tenant_id, email, password_hash, name, role, created_at
		 FROM user_credentials WHERE email = $1`, email,
	).Scan(&cred.ID, &cred.TenantID, &cred.Email, &cred.PasswordHash, &cred.Name, &cred.Role, &cred.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("auth repo: get credential: %w", err)
	}

	if cred.TenantID == nil {
		return &cred, nil, nil, nil
	}

	// Fetch tenant + active API key
	var t domain.Tenant
	var k domain.APIKey
	err = r.pool.QueryRow(ctx, `
		SELECT t.id, t.name, t.created_at,
		       k.id, k.tenant_id, COALESCE(k.raw_key,''), k.key_hash, k.key_prefix, k.active
		FROM tenants t
		JOIN api_keys k ON k.tenant_id = t.id AND k.active = TRUE
		WHERE t.id = $1
		LIMIT 1`, *cred.TenantID,
	).Scan(&t.ID, &t.Name, &t.CreatedAt, &k.ID, &k.TenantID, &k.RawKey, &k.KeyHash, &k.KeyPrefix, &k.Active)
	if errors.Is(err, pgx.ErrNoRows) {
		// Tenant exists but has no active key — return tenant only
		_ = r.pool.QueryRow(ctx,
			`SELECT id, name, created_at FROM tenants WHERE id = $1`, *cred.TenantID,
		).Scan(&t.ID, &t.Name, &t.CreatedAt)
		return &cred, &t, nil, nil
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("auth repo: get tenant+key: %w", err)
	}
	return &cred, &t, &k, nil
}
