package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/systynlabs/vaultnuban/internal/domain"
)

type AuditRepo struct{ pool *pgxpool.Pool }

func NewAuditRepo(pool *pgxpool.Pool) *AuditRepo { return &AuditRepo{pool: pool} }

func (r *AuditRepo) Append(ctx context.Context, e *domain.AuditEntry) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO audit_log(tenant_id, actor, action, entity_type, entity_id, before_after, at)
		VALUES($1,$2,$3,$4,$5,$6,NOW())`,
		e.TenantID, e.Actor, e.Action, e.EntityType, e.EntityID, e.BeforeAfter,
	)
	if err != nil {
		return fmt.Errorf("audit repo: append: %w", err)
	}
	return nil
}

func (r *AuditRepo) ListAuditEntries(ctx context.Context, tenantID string, limit int, cursor string) ([]*domain.AuditEntry, string, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows pgx.Rows
	var err error
	if cursor == "" {
		rows, err = r.pool.Query(ctx, `
			SELECT id, COALESCE(tenant_id::text,''), actor, action, entity_type, entity_id, at
			FROM audit_log
			WHERE tenant_id = $1
			ORDER BY at DESC
			LIMIT $2`, tenantID, limit+1)
	} else {
		rows, err = r.pool.Query(ctx, `
			SELECT id, COALESCE(tenant_id::text,''), actor, action, entity_type, entity_id, at
			FROM audit_log
			WHERE tenant_id = $1 AND at < $2
			ORDER BY at DESC
			LIMIT $3`, tenantID, cursor, limit+1)
	}
	if err != nil {
		return nil, "", fmt.Errorf("audit repo: list: %w", err)
	}
	defer rows.Close()

	var out []*domain.AuditEntry
	for rows.Next() {
		var e domain.AuditEntry
		var tenantIDStr string
		if err := rows.Scan(&e.ID, &tenantIDStr, &e.Actor, &e.Action, &e.EntityType, &e.EntityID, &e.At); err != nil {
			return nil, "", fmt.Errorf("audit repo: list scan: %w", err)
		}
		out = append(out, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var next string
	if len(out) > limit {
		next = out[limit-1].At.UTC().Format("2006-01-02T15:04:05.999999999Z")
		out = out[:limit]
	}
	return out, next, nil
}
