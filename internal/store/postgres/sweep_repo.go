package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/systynlabs/vaultnuban/internal/domain"
)

type SweepRepo struct{ pool *pgxpool.Pool }

func NewSweepRepo(pool *pgxpool.Pool) *SweepRepo { return &SweepRepo{pool: pool} }

func (r *SweepRepo) CreateSweepRun(ctx context.Context, run *domain.SweepRun) error {
	err := r.pool.QueryRow(ctx, `
		INSERT INTO sweep_runs
		    (window_from, window_to, pages_fetched, found, posted, suspensed, duration_ms, error)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING id, ran_at`,
		run.WindowFrom, run.WindowTo,
		run.PagesFetched, run.Found, run.Posted, run.Suspensed,
		run.DurationMS, run.Error,
	).Scan(&run.ID, &run.RanAt)
	if err != nil {
		return fmt.Errorf("sweep repo: create run: %w", err)
	}
	return nil
}

// ListSweepRuns returns the most recent sweep runs, newest first.
func (r *SweepRepo) ListSweepRuns(ctx context.Context, limit int) ([]*domain.SweepRun, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, window_from, window_to, pages_fetched, found, posted, suspensed, duration_ms, error, ran_at
		FROM sweep_runs
		ORDER BY ran_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("sweep repo: list runs: %w", err)
	}
	defer rows.Close()

	var out []*domain.SweepRun
	for rows.Next() {
		var run domain.SweepRun
		if err := rows.Scan(
			&run.ID, &run.WindowFrom, &run.WindowTo,
			&run.PagesFetched, &run.Found, &run.Posted, &run.Suspensed,
			&run.DurationMS, &run.Error, &run.RanAt,
		); err != nil {
			return nil, fmt.Errorf("sweep repo: list runs scan: %w", err)
		}
		out = append(out, &run)
	}
	return out, rows.Err()
}

// GetLastSweepTime returns the ran_at of the most recent successful sweep run.
// Returns zero time if no sweep has ever run.
func (r *SweepRepo) GetLastSweepTime(ctx context.Context) (time.Time, error) {
	var t time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT ran_at FROM sweep_runs
		WHERE error IS NULL
		ORDER BY ran_at DESC
		LIMIT 1`,
	).Scan(&t)
	if err != nil {
		// ErrNoRows → zero time is the correct fallback
		return time.Time{}, nil
	}
	return t, nil
}
