package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SettingsRepo struct{ pool *pgxpool.Pool }

func NewSettingsRepo(pool *pgxpool.Pool) *SettingsRepo { return &SettingsRepo{pool: pool} }

func (r *SettingsRepo) GetSetting(ctx context.Context, key string) ([]byte, error) {
	var value []byte
	err := r.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = $1`, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("settings repo: get %q: %w", key, err)
	}
	return value, nil
}

func (r *SettingsRepo) UpsertSetting(ctx context.Context, key string, value []byte) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO settings(key, value, updated_at)
		VALUES($1, $2, NOW())
		ON CONFLICT(key) DO UPDATE
		  SET value = EXCLUDED.value,
		      updated_at = NOW()`,
		key, value,
	)
	if err != nil {
		return fmt.Errorf("settings repo: upsert %q: %w", key, err)
	}
	return nil
}

func (r *SettingsRepo) SeedSetting(ctx context.Context, key string, value []byte) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO settings(key, value)
		VALUES($1, $2)
		ON CONFLICT(key) DO NOTHING`,
		key, value,
	)
	if err != nil {
		return fmt.Errorf("settings repo: seed %q: %w", key, err)
	}
	return nil
}
