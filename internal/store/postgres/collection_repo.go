package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/systynlabs/vaultnuban/internal/domain"
)

type CollectionRepo struct{ pool *pgxpool.Pool }

func NewCollectionRepo(pool *pgxpool.Pool) *CollectionRepo { return &CollectionRepo{pool: pool} }

func (r *CollectionRepo) CreateCollection(ctx context.Context, c *domain.Collection) error {
	err := r.pool.QueryRow(ctx, `
		INSERT INTO collections(
			customer_id, virtual_account_id, expected_amount_kobo,
			reference, description, status, expires_at
		) VALUES($1,$2,$3,$4,$5,$6,$7)
		RETURNING id, created_at, updated_at`,
		c.CustomerID, c.VirtualAccountID, c.ExpectedAmountKobo,
		c.Reference, c.Description, c.Status, c.ExpiresAt,
	).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return fmt.Errorf("collection repo: create: %w", err)
	}
	return nil
}

func (r *CollectionRepo) GetCollection(ctx context.Context, collectionID string) (*domain.Collection, error) {
	var c domain.Collection
	err := r.pool.QueryRow(ctx, `
		SELECT col.id, col.customer_id, col.virtual_account_id, col.expected_amount_kobo,
		       col.reference, col.description, col.status, col.expires_at,
		       col.fulfilled_by_txn_id, col.fulfilled_at, col.created_at, col.updated_at,
		       COALESCE(va.nuban, ''), COALESCE(va.bank_name, '')
		FROM collections col
		LEFT JOIN virtual_accounts va ON va.id = col.virtual_account_id
		WHERE col.id = $1`, collectionID,
	).Scan(
		&c.ID, &c.CustomerID, &c.VirtualAccountID, &c.ExpectedAmountKobo,
		&c.Reference, &c.Description, &c.Status, &c.ExpiresAt,
		&c.FulfilledByTxnID, &c.FulfilledAt, &c.CreatedAt, &c.UpdatedAt,
		&c.NUBAN, &c.BankName,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("collection repo: get: %w", err)
	}
	return &c, nil
}

func (r *CollectionRepo) ListCollections(
	ctx context.Context,
	customerID string,
	limit int,
	cursor string,
) ([]*domain.Collection, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	query := `
		SELECT col.id, col.customer_id, col.virtual_account_id, col.expected_amount_kobo,
		       col.reference, col.description, col.status, col.expires_at,
		       col.fulfilled_by_txn_id, col.fulfilled_at, col.created_at, col.updated_at,
		       COALESCE(va.nuban, ''), COALESCE(va.bank_name, '')
		FROM collections col
		LEFT JOIN virtual_accounts va ON va.id = col.virtual_account_id
		WHERE col.customer_id = $1`

	var rows pgx.Rows
	var err error
	if cursor == "" {
		rows, err = r.pool.Query(ctx, query+` ORDER BY col.created_at DESC LIMIT $2`, customerID, limit+1)
	} else {
		rows, err = r.pool.Query(ctx, query+`
			AND col.created_at < (SELECT created_at FROM collections WHERE id = $3)
			ORDER BY col.created_at DESC LIMIT $2`,
			customerID, limit+1, cursor)
	}
	if err != nil {
		return nil, "", fmt.Errorf("collection repo: list: %w", err)
	}
	defer rows.Close()

	var out []*domain.Collection
	for rows.Next() {
		var c domain.Collection
		if err := rows.Scan(
			&c.ID, &c.CustomerID, &c.VirtualAccountID, &c.ExpectedAmountKobo,
			&c.Reference, &c.Description, &c.Status, &c.ExpiresAt,
			&c.FulfilledByTxnID, &c.FulfilledAt, &c.CreatedAt, &c.UpdatedAt,
			&c.NUBAN, &c.BankName,
		); err != nil {
			return nil, "", fmt.Errorf("collection repo: scan: %w", err)
		}
		out = append(out, &c)
	}

	var nextCursor string
	if len(out) > limit {
		nextCursor = out[limit-1].ID
		out = out[:limit]
	}
	return out, nextCursor, nil
}

func (r *CollectionRepo) FulfillCollection(ctx context.Context, collectionID, txnID string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE collections
		SET status='fulfilled', fulfilled_by_txn_id=$1, fulfilled_at=NOW(), updated_at=NOW()
		WHERE id=$2 AND status='open'`,
		txnID, collectionID,
	)
	if err != nil {
		return fmt.Errorf("collection repo: fulfill: %w", err)
	}
	return nil
}

func (r *CollectionRepo) CancelCollection(ctx context.Context, collectionID string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE collections
		SET status='cancelled', updated_at=NOW()
		WHERE id=$1 AND status='open'`,
		collectionID,
	)
	if err != nil {
		return fmt.Errorf("collection repo: cancel: %w", err)
	}
	return nil
}

// FindOpenCollectionForVA returns the first open collection for a VA where
// the expected amount matches (or the collection accepts any amount).
func (r *CollectionRepo) FindOpenCollectionForVA(ctx context.Context, vaID string, amountKobo int64) (*domain.Collection, error) {
	var c domain.Collection
	err := r.pool.QueryRow(ctx, `
		SELECT id, customer_id, virtual_account_id, expected_amount_kobo,
		       reference, description, status, expires_at,
		       fulfilled_by_txn_id, fulfilled_at, created_at, updated_at, '', ''
		FROM collections
		WHERE virtual_account_id = $1
		  AND status = 'open'
		  AND (expires_at IS NULL OR expires_at > NOW())
		  AND (expected_amount_kobo IS NULL OR expected_amount_kobo = $2)
		ORDER BY created_at ASC
		LIMIT 1`,
		vaID, amountKobo,
	).Scan(
		&c.ID, &c.CustomerID, &c.VirtualAccountID, &c.ExpectedAmountKobo,
		&c.Reference, &c.Description, &c.Status, &c.ExpiresAt,
		&c.FulfilledByTxnID, &c.FulfilledAt, &c.CreatedAt, &c.UpdatedAt,
		&c.NUBAN, &c.BankName,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("collection repo: find open: %w", err)
	}
	return &c, nil
}
