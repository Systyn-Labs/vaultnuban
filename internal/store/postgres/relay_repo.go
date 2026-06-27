package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/systynlabs/vaultnuban/internal/domain"
)

type RelayRepo struct{ pool *pgxpool.Pool }

func NewRelayRepo(pool *pgxpool.Pool) *RelayRepo { return &RelayRepo{pool: pool} }

func (r *RelayRepo) CreateEndpoint(ctx context.Context, ep *domain.RelayEndpoint) error {
	return r.pool.QueryRow(ctx, `
		INSERT INTO relay_endpoints(tenant_id, url, secret_hash, active)
		VALUES($1,$2,$3,$4)
		RETURNING id, created_at`,
		ep.TenantID, ep.URL, ep.SecretHash, ep.Active,
	).Scan(&ep.ID, &ep.CreatedAt)
}

func (r *RelayRepo) ListEndpoints(ctx context.Context, tenantID string) ([]*domain.RelayEndpoint, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, url, secret_hash, active, created_at
		FROM relay_endpoints
		WHERE tenant_id=$1
		ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("relay repo: list endpoints: %w", err)
	}
	defer rows.Close()

	var out []*domain.RelayEndpoint
	for rows.Next() {
		var ep domain.RelayEndpoint
		if err := rows.Scan(&ep.ID, &ep.TenantID, &ep.URL, &ep.SecretHash, &ep.Active, &ep.CreatedAt); err != nil {
			return nil, fmt.Errorf("relay repo: scan endpoint: %w", err)
		}
		out = append(out, &ep)
	}
	return out, nil
}

func (r *RelayRepo) GetEndpoint(ctx context.Context, id string) (*domain.RelayEndpoint, error) {
	var ep domain.RelayEndpoint
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, url, secret_hash, active, created_at
		FROM relay_endpoints WHERE id=$1`, id,
	).Scan(&ep.ID, &ep.TenantID, &ep.URL, &ep.SecretHash, &ep.Active, &ep.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("relay repo: get endpoint: %w", err)
	}
	return &ep, nil
}

func (r *RelayRepo) DeactivateEndpoint(ctx context.Context, id, tenantID string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE relay_endpoints SET active=FALSE
		WHERE id=$1 AND tenant_id=$2`, id, tenantID)
	return err
}

func (r *RelayRepo) CreateDelivery(ctx context.Context, d *domain.RelayDelivery) error {
	return r.pool.QueryRow(ctx, `
		INSERT INTO relay_deliveries(endpoint_id, event_type, payload, attempt, status, next_retry_at)
		VALUES($1,$2,$3,$4,$5,$6)
		RETURNING id, created_at`,
		d.EndpointID, d.EventType, d.Payload, d.Attempt, d.Status, d.NextRetryAt,
	).Scan(&d.ID, &d.CreatedAt)
}

func (r *RelayRepo) UpdateDelivery(ctx context.Context, d *domain.RelayDelivery) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE relay_deliveries
		SET status=$1, status_code=$2, error=$3, next_retry_at=$4, delivered_at=$5, attempt=$6
		WHERE id=$7`,
		d.Status, d.StatusCode, d.Error, d.NextRetryAt, d.DeliveredAt, d.Attempt, d.ID,
	)
	return err
}

func (r *RelayRepo) ListPendingRetries(ctx context.Context, limit int) ([]*domain.RelayDelivery, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, endpoint_id, event_type, payload, attempt, status,
		       status_code, error, next_retry_at, delivered_at, created_at
		FROM relay_deliveries
		WHERE status = 'failed'
		  AND next_retry_at <= NOW()
		ORDER BY next_retry_at
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("relay repo: list retries: %w", err)
	}
	defer rows.Close()

	var out []*domain.RelayDelivery
	for rows.Next() {
		var d domain.RelayDelivery
		if err := rows.Scan(
			&d.ID, &d.EndpointID, &d.EventType, &d.Payload, &d.Attempt, &d.Status,
			&d.StatusCode, &d.Error, &d.NextRetryAt, &d.DeliveredAt, &d.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("relay repo: scan delivery: %w", err)
		}
		out = append(out, &d)
	}
	return out, nil
}
