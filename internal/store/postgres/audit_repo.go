package postgres

import (
	"context"
	"fmt"

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
