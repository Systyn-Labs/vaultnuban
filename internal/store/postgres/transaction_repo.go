package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/systynlabs/vaultnuban/internal/domain"
)

type TransactionRepo struct{ pool *pgxpool.Pool }

func NewTransactionRepo(pool *pgxpool.Pool) *TransactionRepo { return &TransactionRepo{pool: pool} }

// PostTransaction inserts the transaction and its ledger entries in one DB transaction.
// If the transactionId PK already exists (duplicate webhook / sweep race) it returns
// created=false with no error — the caller skips all side effects.
func (r *TransactionRepo) PostTransaction(
	ctx context.Context,
	tx *domain.Transaction,
	entries []domain.LedgerEntry,
) (bool, error) {
	// Validate the double-entry invariant before touching the DB (NFR-1).
	if err := assertBalanced(entries); err != nil {
		return false, err
	}

	dbtx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("transaction repo: begin: %w", err)
	}
	defer dbtx.Rollback(ctx) //nolint:errcheck

	err = dbtx.QueryRow(ctx, `
		INSERT INTO transactions
		    (id, virtual_account_id, session_id, amount_kobo, direction, source,
		     status, sender_name, sender_bank, narration, raw, occurred_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (id) DO NOTHING
		RETURNING id`,
		tx.ID, tx.VirtualAccountID, tx.SessionID, tx.AmountKobo,
		tx.Direction, tx.Source, tx.Status,
		tx.SenderName, tx.SenderBank, tx.Narration,
		tx.Raw, tx.OccurredAt,
	).Scan(&tx.ID)

	if errors.Is(err, pgx.ErrNoRows) {
		// ON CONFLICT DO NOTHING → row already existed.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("transaction repo: insert transaction: %w", err)
	}

	for _, e := range entries {
		if _, err := dbtx.Exec(ctx, `
			INSERT INTO ledger_entries(transaction_id, account, direction, amount_kobo)
			VALUES($1,$2,$3,$4)`,
			e.TransactionID, e.Account, e.Direction, e.AmountKobo,
		); err != nil {
			return false, fmt.Errorf("transaction repo: insert ledger entry: %w", err)
		}
	}

	if err := dbtx.Commit(ctx); err != nil {
		return false, fmt.Errorf("transaction repo: commit: %w", err)
	}
	return true, nil
}

func (r *TransactionRepo) GetTransaction(ctx context.Context, txID string) (*domain.Transaction, error) {
	var tx domain.Transaction
	err := r.pool.QueryRow(ctx, `
		SELECT id, virtual_account_id, session_id, amount_kobo, direction, source,
		       status, sender_name, sender_bank, narration, raw, occurred_at, created_at
		FROM transactions WHERE id = $1`, txID,
	).Scan(
		&tx.ID, &tx.VirtualAccountID, &tx.SessionID, &tx.AmountKobo,
		&tx.Direction, &tx.Source, &tx.Status,
		&tx.SenderName, &tx.SenderBank, &tx.Narration,
		&tx.Raw, &tx.OccurredAt, &tx.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("transaction repo: get: %w", err)
	}
	return &tx, nil
}

func (r *TransactionRepo) ListTransactions(
	ctx context.Context,
	vaID string,
	limit int,
	cursor string,
) ([]*domain.Transaction, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	var rows pgx.Rows
	var err error
	if cursor == "" {
		rows, err = r.pool.Query(ctx, `
			SELECT id, virtual_account_id, session_id, amount_kobo, direction, source,
			       status, sender_name, sender_bank, narration, raw, occurred_at, created_at
			FROM transactions
			WHERE virtual_account_id = $1
			ORDER BY occurred_at DESC
			LIMIT $2`,
			vaID, limit+1,
		)
	} else {
		rows, err = r.pool.Query(ctx, `
			SELECT id, virtual_account_id, session_id, amount_kobo, direction, source,
			       status, sender_name, sender_bank, narration, raw, occurred_at, created_at
			FROM transactions
			WHERE virtual_account_id = $1 AND occurred_at < (
			    SELECT occurred_at FROM transactions WHERE id = $3
			)
			ORDER BY occurred_at DESC
			LIMIT $2`,
			vaID, limit+1, cursor,
		)
	}
	if err != nil {
		return nil, "", fmt.Errorf("transaction repo: list: %w", err)
	}
	defer rows.Close()

	var txns []*domain.Transaction
	for rows.Next() {
		var tx domain.Transaction
		if err := rows.Scan(
			&tx.ID, &tx.VirtualAccountID, &tx.SessionID, &tx.AmountKobo,
			&tx.Direction, &tx.Source, &tx.Status,
			&tx.SenderName, &tx.SenderBank, &tx.Narration,
			&tx.Raw, &tx.OccurredAt, &tx.CreatedAt,
		); err != nil {
			return nil, "", fmt.Errorf("transaction repo: scan: %w", err)
		}
		txns = append(txns, &tx)
	}

	var nextCursor string
	if len(txns) > limit {
		nextCursor = txns[limit-1].ID
		txns = txns[:limit]
	}
	return txns, nextCursor, nil
}

// GetBalance computes the current ledger balance for an account (credits − debits).
func (r *TransactionRepo) GetBalance(ctx context.Context, account string) (int64, error) {
	var balance int64
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(
		    SUM(CASE WHEN direction = 'credit' THEN amount_kobo ELSE -amount_kobo END),
		0)
		FROM ledger_entries WHERE account = $1`, account,
	).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("transaction repo: get balance: %w", err)
	}
	return balance, nil
}

// GetDailyCredits returns the total credits posted to an account on the given UTC date.
func (r *TransactionRepo) GetDailyCredits(ctx context.Context, account string, date time.Time) (int64, error) {
	dayStart := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	dayEnd := dayStart.Add(24 * time.Hour)

	var total int64
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(le.amount_kobo), 0)
		FROM ledger_entries le
		JOIN transactions t ON t.id = le.transaction_id
		WHERE le.account = $1
		  AND le.direction = 'credit'
		  AND t.occurred_at >= $2
		  AND t.occurred_at < $3`,
		account, dayStart, dayEnd,
	).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("transaction repo: daily credits: %w", err)
	}
	return total, nil
}

func (r *TransactionRepo) GetStatement(
	ctx context.Context,
	account string,
	from, to time.Time,
) (*domain.Statement, error) {
	// Opening balance: everything before `from`
	var opening int64
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(
		    SUM(CASE WHEN le.direction = 'credit' THEN le.amount_kobo ELSE -le.amount_kobo END),
		0)
		FROM ledger_entries le
		JOIN transactions t ON t.id = le.transaction_id
		WHERE le.account = $1 AND t.occurred_at < $2`,
		account, from,
	).Scan(&opening)
	if err != nil {
		return nil, fmt.Errorf("transaction repo: statement opening: %w", err)
	}

	rows, err := r.pool.Query(ctx, `
		SELECT t.occurred_at, t.narration, t.sender_name, t.sender_bank,
		       le.direction, le.amount_kobo
		FROM ledger_entries le
		JOIN transactions t ON t.id = le.transaction_id
		WHERE le.account = $1
		  AND t.occurred_at >= $2
		  AND t.occurred_at < $3
		ORDER BY t.occurred_at ASC`,
		account, from, to,
	)
	if err != nil {
		return nil, fmt.Errorf("transaction repo: statement entries: %w", err)
	}
	defer rows.Close()

	stmt := &domain.Statement{OpeningBalanceKobo: opening, From: from, To: to}
	running := opening
	for rows.Next() {
		var e domain.StatementEntry
		var direction string
		var amountKobo int64
		var narration, senderName, senderBank *string
		if err := rows.Scan(&e.OccurredAt, &narration, &senderName, &senderBank, &direction, &amountKobo); err != nil {
			return nil, fmt.Errorf("transaction repo: statement scan: %w", err)
		}
		desc := ""
		if narration != nil {
			desc = *narration
		}
		if senderName != nil && *senderName != "" {
			desc = *senderName
			if senderBank != nil && *senderBank != "" {
				desc += " / " + *senderBank
			}
		}
		e.Description = desc
		if direction == "credit" {
			e.CreditKobo = amountKobo
			running += amountKobo
		} else {
			e.DebitKobo = amountKobo
			running -= amountKobo
		}
		e.RunningBalance = running
		stmt.Entries = append(stmt.Entries, e)
	}
	stmt.ClosingBalanceKobo = running
	return stmt, nil
}

// assertBalanced enforces NFR-1: Σdebits must equal Σcredits per transaction.
func assertBalanced(entries []domain.LedgerEntry) error {
	var debits, credits int64
	for _, e := range entries {
		switch e.Direction {
		case "debit":
			debits += e.AmountKobo
		case "credit":
			credits += e.AmountKobo
		}
	}
	if debits != credits {
		return fmt.Errorf("ledger: unbalanced entries — debits=%d credits=%d", debits, credits)
	}
	return nil
}
