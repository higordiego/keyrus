// Package migrations owns schema changes for the Ledger bounded context.
package migrations

import (
	"context"
	"embed"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.up.sql *.down.sql
var files embed.FS

var orderedUp = []string{"000001_ledger_core.up.sql"}

// Apply runs pending Ledger migrations under a context-specific advisory lock.
func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin ledger migrations: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `
CREATE SCHEMA IF NOT EXISTS ledger;
CREATE TABLE IF NOT EXISTS ledger.schema_migration (
    version text PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);
SELECT pg_advisory_xact_lock(hashtext('ledger:schema-migrations'));`); err != nil {
		return fmt.Errorf("prepare ledger migrations: %w", err)
	}
	for _, name := range orderedUp {
		var applied bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM ledger.schema_migration WHERE version = $1)`, name,
		).Scan(&applied); err != nil {
			return fmt.Errorf("read ledger migration %s: %w", name, err)
		}
		if applied {
			continue
		}
		sql, err := files.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read ledger migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply ledger migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO ledger.schema_migration (version) VALUES ($1)`, name,
		); err != nil {
			return fmt.Errorf("record ledger migration %s: %w", name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit ledger migrations: %w", err)
	}
	return nil
}
