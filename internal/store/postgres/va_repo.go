package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/systynlabs/vaultnuban/internal/domain"
)

type VARepo struct{ pool *pgxpool.Pool }

func NewVARepo(pool *pgxpool.Pool) *VARepo { return &VARepo{pool: pool} }

func (r *VARepo) CreateVirtualAccount(ctx context.Context, va *domain.VirtualAccount) error {
	err := r.pool.QueryRow(ctx, `
		INSERT INTO virtual_accounts
		    (customer_id, nomba_account_ref, nuban, bank_name, account_name, nomba_holder_id, status)
		VALUES($1,$2,$3,$4,$5,$6,$7)
		RETURNING id, created_at, updated_at`,
		va.CustomerID, va.NombaAccountRef, va.NUBAN, va.BankName,
		va.AccountName, va.NombaHolderID, string(va.Status),
	).Scan(&va.ID, &va.CreatedAt, &va.UpdatedAt)
	if err != nil {
		return fmt.Errorf("va repo: create: %w", err)
	}
	return nil
}

// GetActiveVA returns the single ACTIVE virtual account for a customer, or nil.
func (r *VARepo) GetActiveVA(ctx context.Context, customerID string) (*domain.VirtualAccount, error) {
	va, err := r.scan(ctx, `
		SELECT id, customer_id, nomba_account_ref, nuban, bank_name, account_name,
		       COALESCE(nomba_holder_id,''), status, created_at, updated_at
		FROM virtual_accounts
		WHERE customer_id = $1 AND status = 'ACTIVE'`,
		customerID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return va, err
}

func (r *VARepo) GetVAByNUBAN(ctx context.Context, nuban string) (*domain.VirtualAccount, error) {
	va, err := r.scan(ctx, `
		SELECT id, customer_id, nomba_account_ref, nuban, bank_name, account_name,
		       COALESCE(nomba_holder_id,''), status, created_at, updated_at
		FROM virtual_accounts WHERE nuban = $1`,
		nuban,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return va, err
}

func (r *VARepo) GetVAByAccountRef(ctx context.Context, accountRef string) (*domain.VirtualAccount, error) {
	va, err := r.scan(ctx, `
		SELECT id, customer_id, nomba_account_ref, nuban, bank_name, account_name,
		       COALESCE(nomba_holder_id,''), status, created_at, updated_at
		FROM virtual_accounts WHERE nomba_account_ref = $1`,
		accountRef,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return va, err
}

func (r *VARepo) GetVAByCustomerAndStatus(ctx context.Context, customerID, status string) (*domain.VirtualAccount, error) {
	va, err := r.scan(ctx, `
		SELECT id, customer_id, nomba_account_ref, nuban, bank_name, account_name,
		       COALESCE(nomba_holder_id,''), status, created_at, updated_at
		FROM virtual_accounts
		WHERE customer_id = $1 AND status = $2`,
		customerID, status,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return va, err
}

func (r *VARepo) UpdateVAStatus(ctx context.Context, vaID, status, _ string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE virtual_accounts SET status=$1, updated_at=NOW() WHERE id=$2`,
		status, vaID,
	)
	if err != nil {
		return fmt.Errorf("va repo: update status: %w", err)
	}
	return nil
}

func (r *VARepo) RenameVA(ctx context.Context, vaID, newName, _ string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE virtual_accounts SET account_name=$1, updated_at=NOW() WHERE id=$2`,
		newName, vaID,
	)
	if err != nil {
		return fmt.Errorf("va repo: rename: %w", err)
	}
	return nil
}

func (r *VARepo) scan(ctx context.Context, query string, args ...any) (*domain.VirtualAccount, error) {
	var va domain.VirtualAccount
	var status string
	err := r.pool.QueryRow(ctx, query, args...).Scan(
		&va.ID, &va.CustomerID, &va.NombaAccountRef, &va.NUBAN, &va.BankName,
		&va.AccountName, &va.NombaHolderID, &status, &va.CreatedAt, &va.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	va.Status = domain.VAStatus(status)
	return &va, nil
}

func (r *VARepo) ListVAs(ctx context.Context, tenantID string, limit int, cursor string) ([]*domain.VirtualAccount, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	base := `
		SELECT va.id, va.customer_id, va.nomba_account_ref, va.nuban, va.bank_name,
		       va.account_name, va.nomba_holder_id, va.status, va.created_at, va.updated_at,
		       COALESCE(c.display_name, '')
		FROM virtual_accounts va
		JOIN customers c ON c.id = va.customer_id
		WHERE c.tenant_id = $1`

	var rows pgx.Rows
	var err error
	if cursor == "" {
		rows, err = r.pool.Query(ctx, base+` ORDER BY va.created_at DESC LIMIT $2`, tenantID, limit+1)
	} else {
		rows, err = r.pool.Query(ctx, base+`
			AND va.created_at < (SELECT created_at FROM virtual_accounts WHERE id = $3)
			ORDER BY va.created_at DESC LIMIT $2`, tenantID, limit+1, cursor)
	}
	if err != nil {
		return nil, "", fmt.Errorf("va repo: list: %w", err)
	}
	defer rows.Close()

	var items []*domain.VirtualAccount
	for rows.Next() {
		var va domain.VirtualAccount
		var status string
		if err := rows.Scan(
			&va.ID, &va.CustomerID, &va.NombaAccountRef, &va.NUBAN, &va.BankName,
			&va.AccountName, &va.NombaHolderID, &status, &va.CreatedAt, &va.UpdatedAt,
			&va.CustomerDisplayName,
		); err != nil {
			return nil, "", fmt.Errorf("va repo: list scan: %w", err)
		}
		va.Status = domain.VAStatus(status)
		items = append(items, &va)
	}

	var nextCursor string
	if len(items) > limit {
		nextCursor = items[limit-1].ID
		items = items[:limit]
	}
	return items, nextCursor, nil
}
