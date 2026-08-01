package postgres

import (
	"context"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/higordiegoti/keyrus/services/consolidation/migrations"
)

func ApplyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return fmt.Errorf("consolidation migration pool is required")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin consolidation migrations: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('consolidation:schema-migrations'))`); err != nil {
		return fmt.Errorf("lock consolidation migrations: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS consolidation;
		CREATE TABLE IF NOT EXISTS consolidation.schema_migration (
			name TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
		)`); err != nil {
		return fmt.Errorf("initialize consolidation migration history: %w", err)
	}

	files, err := fs.Glob(migrations.FS, "*.up.sql")
	if err != nil {
		return fmt.Errorf("list consolidation migrations: %w", err)
	}
	sort.Strings(files)
	for _, name := range files {
		var applied bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM consolidation.schema_migration WHERE name = $1
			)`, name).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if applied {
			continue
		}

		sql, err := migrations.FS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err = tx.Exec(ctx, string(sql)); err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO consolidation.schema_migration (name) VALUES ($1)`, name)
		}
		if err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit consolidation migrations: %w", err)
	}
	return nil
}
