package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/systynlabs/vaultnuban/internal/domain"
)

type WebhookRepo struct{ pool *pgxpool.Pool }

func NewWebhookRepo(pool *pgxpool.Pool) *WebhookRepo { return &WebhookRepo{pool: pool} }

// InsertWebhookEvent attempts to insert the event.
// Returns inserted=false (no error) when the dedupe_key already exists (FR-4.3).
func (r *WebhookRepo) InsertWebhookEvent(ctx context.Context, evt *domain.WebhookEvent) (bool, error) {
	err := r.pool.QueryRow(ctx, `
		INSERT INTO webhook_events(dedupe_key, event_type, signature_valid, status, payload)
		VALUES($1,$2,$3,$4,$5)
		ON CONFLICT (dedupe_key) DO NOTHING
		RETURNING id`,
		evt.DedupeKey, evt.EventType, evt.SignatureValid, evt.Status, evt.Payload,
	).Scan(&evt.ID)

	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // duplicate
	}
	if err != nil {
		return false, fmt.Errorf("webhook repo: insert: %w", err)
	}
	return true, nil
}

func (r *WebhookRepo) MarkWebhookProcessed(ctx context.Context, id, status string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE webhook_events
		SET status=$1, processed_at=NOW()
		WHERE id=$2`,
		status, id,
	)
	if err != nil {
		return fmt.Errorf("webhook repo: mark processed: %w", err)
	}
	return nil
}
