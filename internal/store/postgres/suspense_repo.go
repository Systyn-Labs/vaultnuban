package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/systynlabs/vaultnuban/internal/domain"
)

type SuspenseRepo struct{ pool *pgxpool.Pool }

func NewSuspenseRepo(pool *pgxpool.Pool) *SuspenseRepo { return &SuspenseRepo{pool: pool} }

func (r *SuspenseRepo) CreateSuspenseItem(ctx context.Context, item *domain.SuspenseItem) error {
	err := r.pool.QueryRow(ctx, `
		INSERT INTO suspense_items(transaction_id, reason, status)
		VALUES($1,$2,$3)
		RETURNING id, created_at`,
		item.TransactionID, string(item.Reason), item.Status,
	).Scan(&item.ID, &item.CreatedAt)
	if err != nil {
		return fmt.Errorf("suspense repo: create: %w", err)
	}
	return nil
}

func (r *SuspenseRepo) GetSuspenseItem(ctx context.Context, itemID string) (*domain.SuspenseItem, error) {
	var s domain.SuspenseItem
	var reason string
	err := r.pool.QueryRow(ctx, `
		SELECT id, transaction_id, reason, status, resolved_by, resolved_at, notes, created_at
		FROM suspense_items WHERE id = $1`, itemID,
	).Scan(&s.ID, &s.TransactionID, &reason, &s.Status,
		&s.ResolvedBy, &s.ResolvedAt, &s.Notes, &s.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("suspense repo: get: %w", err)
	}
	s.Reason = domain.SuspenseReason(reason)
	return &s, nil
}

func (r *SuspenseRepo) ListSuspenseItems(
	ctx context.Context,
	tenantID string,
	limit int,
	cursor string,
) ([]*domain.SuspenseItem, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	// Join through transactions → virtual_accounts → customers to scope by tenant.
	query := `
		SELECT s.id, s.transaction_id, s.reason, s.status,
		       s.resolved_by, s.resolved_at, s.notes, s.created_at
		FROM suspense_items s
		JOIN transactions t ON t.id = s.transaction_id
		LEFT JOIN virtual_accounts va ON va.id = t.virtual_account_id
		LEFT JOIN customers c ON c.id = va.customer_id
		WHERE (c.tenant_id = $1 OR c.tenant_id IS NULL)
		  AND s.status = 'open'`

	var rows pgx.Rows
	var err error
	if cursor == "" {
		rows, err = r.pool.Query(ctx, query+` ORDER BY s.created_at DESC LIMIT $2`,
			tenantID, limit+1)
	} else {
		rows, err = r.pool.Query(ctx, query+`
			AND s.created_at < (SELECT created_at FROM suspense_items WHERE id = $3)
			ORDER BY s.created_at DESC LIMIT $2`,
			tenantID, limit+1, cursor)
	}
	if err != nil {
		return nil, "", fmt.Errorf("suspense repo: list: %w", err)
	}
	defer rows.Close()

	var items []*domain.SuspenseItem
	for rows.Next() {
		var s domain.SuspenseItem
		var reason string
		if err := rows.Scan(&s.ID, &s.TransactionID, &reason, &s.Status,
			&s.ResolvedBy, &s.ResolvedAt, &s.Notes, &s.CreatedAt); err != nil {
			return nil, "", fmt.Errorf("suspense repo: scan: %w", err)
		}
		s.Reason = domain.SuspenseReason(reason)
		items = append(items, &s)
	}

	var nextCursor string
	if len(items) > limit {
		nextCursor = items[limit-1].ID
		items = items[:limit]
	}
	return items, nextCursor, nil
}

func (r *SuspenseRepo) ResolveSuspenseItem(
	ctx context.Context,
	itemID, resolution, actor, notes string,
) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE suspense_items
		SET status=$1, resolved_by=$2, notes=$3, resolved_at=NOW()
		WHERE id=$4 AND status='open'`,
		resolution, actor, notes, itemID,
	)
	if err != nil {
		return fmt.Errorf("suspense repo: resolve: %w", err)
	}
	return nil
}
