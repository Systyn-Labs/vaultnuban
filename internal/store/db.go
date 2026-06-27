package store

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Open creates a pgxpool connection pool from the given DATABASE_URL.
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return pool, nil
}

// RunMigrations applies all pending *.up.sql migrations in numbered order.
// Applied migrations are tracked in a schema_migrations table.
func RunMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("store: read migrations dir: %w", err)
	}

	// Collect and sort *.up.sql files by filename (001_, 002_, …).
	var upFiles []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			upFiles = append(upFiles, e.Name())
		}
	}
	sort.Strings(upFiles)

	for _, name := range upFiles {
		version := strings.TrimSuffix(name, ".up.sql")

		var exists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, version,
		).Scan(&exists)
		if err != nil {
			return fmt.Errorf("store: check migration %s: %w", version, err)
		}
		if exists {
			continue
		}

		sql, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("store: read migration %s: %w", name, err)
		}

		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("store: apply migration %s: %w", name, err)
		}

		if _, err := pool.Exec(ctx,
			`INSERT INTO schema_migrations(version) VALUES($1)`, version,
		); err != nil {
			return fmt.Errorf("store: record migration %s: %w", name, err)
		}
	}

	return nil
}
