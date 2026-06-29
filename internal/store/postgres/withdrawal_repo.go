package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/systynlabs/vaultnuban/internal/domain"
)

type WithdrawalRepo struct{ pool *pgxpool.Pool }

func NewWithdrawalRepo(pool *pgxpool.Pool) *WithdrawalRepo { return &WithdrawalRepo{pool: pool} }

func (r *WithdrawalRepo) CreateWithdrawal(ctx context.Context, w *domain.Withdrawal) error {
	err := r.pool.QueryRow(ctx, `
		INSERT INTO withdrawals(
			customer_id, virtual_account_id, amount_kobo,
			destination_bank_code, destination_account_number, destination_account_name,
			narration, status, raw
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING id, created_at, updated_at`,
		w.CustomerID, w.VirtualAccountID, w.AmountKobo,
		w.DestinationBankCode, w.DestinationAccountNumber, w.DestinationAccountName,
		w.Narration, w.Status, w.Raw,
	).Scan(&w.ID, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		return fmt.Errorf("withdrawal repo: create: %w", err)
	}
	return nil
}

func (r *WithdrawalRepo) GetWithdrawal(ctx context.Context, withdrawalID string) (*domain.Withdrawal, error) {
	var w domain.Withdrawal
	err := r.pool.QueryRow(ctx, `
		SELECT id, customer_id, virtual_account_id, amount_kobo,
		       destination_bank_code, destination_account_number, destination_account_name,
		       narration, status, provider_transaction_id, provider_session_id,
		       failure_reason, raw, created_at, updated_at
		FROM withdrawals WHERE id = $1`, withdrawalID,
	).Scan(
		&w.ID, &w.CustomerID, &w.VirtualAccountID, &w.AmountKobo,
		&w.DestinationBankCode, &w.DestinationAccountNumber, &w.DestinationAccountName,
		&w.Narration, &w.Status, &w.ProviderTransactionID, &w.ProviderSessionID,
		&w.FailureReason, &w.Raw, &w.CreatedAt, &w.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("withdrawal repo: get: %w", err)
	}
	return &w, nil
}

func (r *WithdrawalRepo) UpdateWithdrawalStatus(
	ctx context.Context,
	withdrawalID, status string,
	provTxID, provSessionID, failureReason *string,
) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE withdrawals
		SET status=$1,
		    provider_transaction_id=COALESCE($2, provider_transaction_id),
		    provider_session_id=COALESCE($3, provider_session_id),
		    failure_reason=COALESCE($4, failure_reason),
		    updated_at=NOW()
		WHERE id=$5`,
		status, provTxID, provSessionID, failureReason, withdrawalID,
	)
	if err != nil {
		return fmt.Errorf("withdrawal repo: update status: %w", err)
	}
	return nil
}

func (r *WithdrawalRepo) ListWithdrawals(
	ctx context.Context,
	customerID string,
	limit int,
	cursor string,
) ([]*domain.Withdrawal, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	query := `
		SELECT id, customer_id, virtual_account_id, amount_kobo,
		       destination_bank_code, destination_account_number, destination_account_name,
		       narration, status, provider_transaction_id, provider_session_id,
		       failure_reason, raw, created_at, updated_at
		FROM withdrawals
		WHERE customer_id = $1`

	var rows pgx.Rows
	var err error
	if cursor == "" {
		rows, err = r.pool.Query(ctx, query+` ORDER BY created_at DESC LIMIT $2`, customerID, limit+1)
	} else {
		rows, err = r.pool.Query(ctx, query+`
			AND created_at < (SELECT created_at FROM withdrawals WHERE id = $3)
			ORDER BY created_at DESC LIMIT $2`,
			customerID, limit+1, cursor)
	}
	if err != nil {
		return nil, "", fmt.Errorf("withdrawal repo: list: %w", err)
	}
	defer rows.Close()

	var out []*domain.Withdrawal
	for rows.Next() {
		var w domain.Withdrawal
		if err := rows.Scan(
			&w.ID, &w.CustomerID, &w.VirtualAccountID, &w.AmountKobo,
			&w.DestinationBankCode, &w.DestinationAccountNumber, &w.DestinationAccountName,
			&w.Narration, &w.Status, &w.ProviderTransactionID, &w.ProviderSessionID,
			&w.FailureReason, &w.Raw, &w.CreatedAt, &w.UpdatedAt,
		); err != nil {
			return nil, "", fmt.Errorf("withdrawal repo: scan: %w", err)
		}
		out = append(out, &w)
	}

	var nextCursor string
	if len(out) > limit {
		nextCursor = out[limit-1].ID
		out = out[:limit]
	}
	return out, nextCursor, nil
}
