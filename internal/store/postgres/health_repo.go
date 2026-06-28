package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/systynlabs/vaultnuban/internal/domain"
)

type HealthRepo struct{ pool *pgxpool.Pool }

func NewHealthRepo(pool *pgxpool.Pool) *HealthRepo { return &HealthRepo{pool: pool} }

func (r *HealthRepo) GetPlatformHealth(ctx context.Context) (*domain.PlatformHealth, error) {
	h := &domain.PlatformHealth{CheckedAt: time.Now()}

	// ── Ledger invariant ──────────────────────────────────────────────────────
	if err := r.pool.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(amount_kobo) FILTER (WHERE direction = 'debit'),  0),
			COALESCE(SUM(amount_kobo) FILTER (WHERE direction = 'credit'), 0)
		FROM ledger_entries`,
	).Scan(&h.Ledger.DebitsKobo, &h.Ledger.CreditsKobo); err != nil {
		return nil, err
	}
	h.Ledger.Balanced = h.Ledger.DebitsKobo == h.Ledger.CreditsKobo

	// ── Last sweep run ────────────────────────────────────────────────────────
	var snap domain.SweepHealthSnapshot
	err := r.pool.QueryRow(ctx, `
		SELECT posted, found, suspensed, ran_at
		FROM sweep_runs
		WHERE error IS NULL
		ORDER BY ran_at DESC
		LIMIT 1`,
	).Scan(&snap.Posted, &snap.Found, &snap.Suspensed, &snap.RanAt)
	if err == nil {
		h.LastSweep = &snap
	} // no rows → h.LastSweep stays nil

	// ── Webhook success 24h ───────────────────────────────────────────────────
	if err := r.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE status = 'delivered'),
			COUNT(*)
		FROM relay_deliveries
		WHERE created_at > NOW() - INTERVAL '24 hours'`,
	).Scan(&h.Webhook24h.Delivered, &h.Webhook24h.Total); err != nil {
		return nil, err
	}

	// ── Cross-tenant suspense ─────────────────────────────────────────────────
	if err := r.pool.QueryRow(ctx, `
		SELECT
			COUNT(si.id),
			COALESCE(SUM(t.amount_kobo), 0),
			COUNT(DISTINCT c.tenant_id)
		FROM suspense_items si
		JOIN transactions t ON t.id = si.transaction_id
		LEFT JOIN virtual_accounts va ON va.id = t.virtual_account_id
		LEFT JOIN customers c ON c.id = va.customer_id
		WHERE si.status = 'open'`,
	).Scan(&h.CrossTenantSuspense.ItemCount, &h.CrossTenantSuspense.AmountKobo, &h.CrossTenantSuspense.TenantCount); err != nil {
		return nil, err
	}

	// ── Active / total tenants ────────────────────────────────────────────────
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM tenants`).Scan(&h.TotalTenants); err != nil {
		return nil, err
	}
	h.ActiveTenants = h.TotalTenants

	// ── Per-tenant health ─────────────────────────────────────────────────────
	rows, err := r.pool.Query(ctx, `
		SELECT
			ten.id,
			ten.name,
			COUNT(DISTINCT c.id)                                              AS customers,
			COUNT(DISTINCT va.id)                                             AS accounts,
			COALESCE(SUM(t.amount_kobo) FILTER (WHERE si.status = 'open'), 0) AS open_suspense_kobo,
			MAX(t.occurred_at)                                                AS last_activity
		FROM tenants ten
		LEFT JOIN customers c   ON c.tenant_id = ten.id
		LEFT JOIN virtual_accounts va ON va.customer_id = c.id
		LEFT JOIN transactions t ON t.virtual_account_id = va.id
		LEFT JOIN suspense_items si ON si.transaction_id = t.id
		GROUP BY ten.id, ten.name
		ORDER BY ten.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var th domain.TenantHealth
		var lastAct *time.Time
		if err := rows.Scan(&th.ID, &th.Name, &th.Customers, &th.Accounts, &th.OpenSuspenseKobo, &lastAct); err != nil {
			return nil, err
		}
		th.LastActivity = lastAct
		th.Status = "active"
		h.TenantHealth = append(h.TenantHealth, th)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return h, nil
}

func (r *HealthRepo) ListCrossTenantSuspense(ctx context.Context, limit int, cursor string) ([]*domain.CrossTenantSuspenseItem, string, error) {
	if limit <= 0 {
		limit = 100
	}
	var q string
	var args []any
	if cursor == "" {
		q = `
			SELECT si.id, si.transaction_id, si.reason, si.status, si.created_at,
			       t.amount_kobo, COALESCE(va.nuban,'') AS nuban,
			       COALESCE(ten.name,'') AS tenant_name
			FROM suspense_items si
			JOIN transactions t ON t.id = si.transaction_id
			LEFT JOIN virtual_accounts va ON va.id = t.virtual_account_id
			LEFT JOIN customers c ON c.id = va.customer_id
			LEFT JOIN tenants ten ON ten.id = c.tenant_id
			WHERE si.status = 'open'
			ORDER BY si.created_at DESC
			LIMIT $1`
		args = []any{limit + 1}
	} else {
		q = `
			SELECT si.id, si.transaction_id, si.reason, si.status, si.created_at,
			       t.amount_kobo, COALESCE(va.nuban,'') AS nuban,
			       COALESCE(ten.name,'') AS tenant_name
			FROM suspense_items si
			JOIN transactions t ON t.id = si.transaction_id
			LEFT JOIN virtual_accounts va ON va.id = t.virtual_account_id
			LEFT JOIN customers c ON c.id = va.customer_id
			LEFT JOIN tenants ten ON ten.id = c.tenant_id
			WHERE si.status = 'open' AND si.created_at < $1
			ORDER BY si.created_at DESC
			LIMIT $2`
		args = []any{cursor, limit + 1}
	}

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", fmt.Errorf("health repo: list cross-tenant suspense: %w", err)
	}
	defer rows.Close()

	var out []*domain.CrossTenantSuspenseItem
	for rows.Next() {
		var item domain.CrossTenantSuspenseItem
		var reason string
		if err := rows.Scan(
			&item.ID, &item.TransactionID, &reason, &item.Status, &item.CreatedAt,
			&item.AmountKobo, &item.NUBAN, &item.TenantName,
		); err != nil {
			return nil, "", fmt.Errorf("health repo: list xts scan: %w", err)
		}
		item.Reason = domain.SuspenseReason(reason)
		out = append(out, &item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var next string
	if len(out) > limit {
		next = out[limit-1].CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z")
		out = out[:limit]
	}
	return out, next, nil
}
