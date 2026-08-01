// Package migrations owns schema changes for the Ledger bounded context.
package migrations

import (
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.up.sql *.down.sql
var files embed.FS

type migration struct {
	up   string
	down string
}

var ordered = []migration{{
	up:   "000001_ledger_core.up.sql",
	down: "000001_ledger_core.down.sql",
}}

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
    checksum character(64) NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE ledger.schema_migration
    ADD COLUMN IF NOT EXISTS checksum character(64);
SELECT pg_advisory_xact_lock(hashtext('ledger:schema-migrations'));`); err != nil {
		return fmt.Errorf("prepare ledger migrations: %w", err)
	}
	for _, migration := range ordered {
		sql, err := files.ReadFile(migration.up)
		if err != nil {
			return fmt.Errorf("read ledger migration %s: %w", migration.up, err)
		}
		checksum := migrationChecksum(sql)
		var storedChecksum *string
		err = tx.QueryRow(ctx,
			`SELECT checksum FROM ledger.schema_migration WHERE version = $1`, migration.up,
		).Scan(&storedChecksum)
		if err == nil {
			if storedChecksum == nil || *storedChecksum != checksum {
				return fmt.Errorf("ledger migration %s checksum mismatch", migration.up)
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("read ledger migration %s: %w", migration.up, err)
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply ledger migration %s: %w", migration.up, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO ledger.schema_migration (version, checksum) VALUES ($1, $2)`, migration.up, checksum,
		); err != nil {
			return fmt.Errorf("record ledger migration %s: %w", migration.up, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit ledger migrations: %w", err)
	}
	return nil
}

// RollbackAll irreversibly deletes every Ledger table and all financial data.
// It is restricted to disposable development databases or explicitly approved
// recovery procedures. Production deployment must use forward migrations.
func RollbackAll(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin destructive ledger rollback: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var trackerExists bool
	if err := tx.QueryRow(ctx,
		`SELECT to_regclass('ledger.schema_migration') IS NOT NULL`,
	).Scan(&trackerExists); err != nil {
		return fmt.Errorf("inspect ledger migration tracker: %w", err)
	}
	if !trackerExists {
		return nil
	}
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext('ledger:schema-migrations'))`,
	); err != nil {
		return fmt.Errorf("lock destructive ledger rollback: %w", err)
	}
	for index := len(ordered) - 1; index >= 0; index-- {
		migration := ordered[index]
		upSQL, err := files.ReadFile(migration.up)
		if err != nil {
			return fmt.Errorf("read ledger migration %s: %w", migration.up, err)
		}
		var storedChecksum *string
		err = tx.QueryRow(ctx,
			`SELECT checksum FROM ledger.schema_migration WHERE version = $1`, migration.up,
		).Scan(&storedChecksum)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read ledger migration %s: %w", migration.up, err)
		}
		if storedChecksum == nil || *storedChecksum != migrationChecksum(upSQL) {
			return fmt.Errorf("ledger migration %s checksum mismatch", migration.up)
		}
		downSQL, err := files.ReadFile(migration.down)
		if err != nil {
			return fmt.Errorf("read ledger rollback %s: %w", migration.down, err)
		}
		if _, err := tx.Exec(ctx, string(downSQL)); err != nil {
			return fmt.Errorf("apply destructive ledger rollback %s: %w", migration.down, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit destructive ledger rollback: %w", err)
	}
	return nil
}

func migrationChecksum(sql []byte) string {
	checksum := sha256.Sum256(sql)
	return fmt.Sprintf("%x", checksum)
}
