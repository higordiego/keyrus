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

const (
	legacyMigration = "000001_ledger_core.up.sql"
	legacyChecksum  = "7df6b8f3cc82408ae9c53e99d7e7667e55a0431bc54e8de8e8cf7238989d4417"
)

var ordered = []migration{
	{up: legacyMigration, down: "000001_ledger_core.down.sql"},
	{up: "000002_ledger_integrity.up.sql", down: "000002_ledger_integrity.down.sql"},
}

// Apply runs pending Ledger migrations under a context-specific advisory lock.
func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin ledger migrations: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	legacySQL, err := files.ReadFile(legacyMigration)
	if err != nil {
		return fmt.Errorf("read published ledger migration %s: %w", legacyMigration, err)
	}
	if migrationChecksum(legacySQL) != legacyChecksum {
		return fmt.Errorf("published ledger migration %s was modified", legacyMigration)
	}
	if _, err := tx.Exec(ctx, `
CREATE SCHEMA IF NOT EXISTS ledger;
CREATE TABLE IF NOT EXISTS ledger.schema_migration (
    version text PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);
SELECT pg_advisory_xact_lock(hashtext('ledger:schema-migrations'));`); err != nil {
		return fmt.Errorf("prepare ledger migrations: %w", err)
	}
	hasChecksum, err := trackerHasChecksum(ctx, tx)
	if err != nil {
		return err
	}
	if !hasChecksum {
		if err := validateLegacyTracker(ctx, tx); err != nil {
			return err
		}
	}
	for _, migration := range ordered {
		sql, err := files.ReadFile(migration.up)
		if err != nil {
			return fmt.Errorf("read ledger migration %s: %w", migration.up, err)
		}
		checksum := migrationChecksum(sql)
		if hasChecksum {
			var storedChecksum string
			err = tx.QueryRow(ctx,
				`SELECT checksum FROM ledger.schema_migration WHERE version = $1`, migration.up,
			).Scan(&storedChecksum)
			if err == nil {
				if storedChecksum != checksum {
					return fmt.Errorf("ledger migration %s checksum mismatch", migration.up)
				}
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("read ledger migration %s: %w", migration.up, err)
			}
		} else {
			var applied bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM ledger.schema_migration WHERE version = $1)`, migration.up,
			).Scan(&applied); err != nil {
				return fmt.Errorf("read legacy ledger migration %s: %w", migration.up, err)
			}
			if applied {
				continue
			}
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply ledger migration %s: %w", migration.up, err)
		}
		hasChecksum, err = trackerHasChecksum(ctx, tx)
		if err != nil {
			return err
		}
		insert := `INSERT INTO ledger.schema_migration (version) VALUES ($1)`
		arguments := []any{migration.up}
		if hasChecksum {
			insert = `INSERT INTO ledger.schema_migration (version, checksum) VALUES ($1, $2)`
			arguments = append(arguments, checksum)
		}
		if _, err := tx.Exec(ctx, insert, arguments...); err != nil {
			return fmt.Errorf("record ledger migration %s: %w", migration.up, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit ledger migrations: %w", err)
	}
	return nil
}

func trackerHasChecksum(ctx context.Context, tx pgx.Tx) (bool, error) {
	var exists bool
	if err := tx.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM information_schema.columns
    WHERE table_schema = 'ledger'
      AND table_name = 'schema_migration'
      AND column_name = 'checksum'
)`).Scan(&exists); err != nil {
		return false, fmt.Errorf("inspect ledger migration checksum column: %w", err)
	}
	return exists, nil
}

func validateLegacyTracker(ctx context.Context, tx pgx.Tx) error {
	var count int
	var first string
	if err := tx.QueryRow(ctx, `
SELECT count(*), COALESCE(min(version), '')
FROM ledger.schema_migration`).Scan(&count, &first); err != nil {
		return fmt.Errorf("inspect legacy ledger migration tracker: %w", err)
	}
	if count == 0 {
		return nil
	}
	if count != 1 || first != legacyMigration {
		return fmt.Errorf("unrecognized legacy ledger migration state")
	}
	var trusted bool
	if err := tx.QueryRow(ctx, `
SELECT to_regclass('ledger.merchant_position') IS NOT NULL
   AND to_regclass('ledger.ledger_entry') IS NOT NULL
   AND to_regclass('ledger.idempotency_record') IS NOT NULL
   AND to_regclass('ledger.outbox_event') IS NOT NULL
   AND EXISTS (
       SELECT 1 FROM pg_constraint
       WHERE conrelid = to_regclass('ledger.idempotency_record')
         AND conname = 'idempotency_record_entry_id_fkey'
         AND contype = 'f'
   )
   AND EXISTS (
       SELECT 1 FROM pg_constraint
       WHERE conrelid = to_regclass('ledger.outbox_event')
         AND conname = 'outbox_event_aggregate_id_fkey'
         AND contype = 'f'
   )
   AND EXISTS (
       SELECT 1 FROM pg_constraint
       WHERE conrelid = to_regclass('ledger.outbox_event')
         AND conname = 'outbox_event_merchant_position_fk'
         AND contype = 'f'
   )
   AND EXISTS (
       SELECT 1
       FROM pg_trigger t
       JOIN pg_proc p ON p.oid = t.tgfoid
       JOIN pg_language l ON l.oid = p.prolang
       WHERE t.tgrelid = to_regclass('ledger.ledger_entry')
         AND t.tgname = 'ledger_entry_immutable'
         AND NOT t.tgisinternal
         AND t.tgenabled = 'O'
         AND t.tgtype = 27
         AND t.tgfoid = to_regprocedure('ledger.reject_ledger_entry_mutation()')
         AND l.lanname = 'plpgsql'
         AND p.prorettype = 'trigger'::regtype
         AND p.pronargs = 0
         AND NOT p.prosecdef
         AND btrim(regexp_replace(
             p.prosrc, '[[:space:]]+', ' ', 'g'
         )) = 'BEGIN RAISE EXCEPTION ''ledger entries are immutable'' USING ERRCODE = ''55000''; END;'
   )
   AND NOT EXISTS (
       SELECT 1 FROM pg_constraint
       WHERE conrelid IN (
           to_regclass('ledger.idempotency_record'),
           to_regclass('ledger.ledger_entry'),
           to_regclass('ledger.outbox_event')
       )
         AND conname IN (
           'idempotency_record_entry_same_merchant_fk',
           'ledger_entry_merchant_id_position_unique',
           'outbox_event_entry_correlation_fk'
       )
   )`).Scan(&trusted); err != nil {
		return fmt.Errorf("validate legacy ledger schema: %w", err)
	}
	if !trusted {
		return fmt.Errorf("unrecognized legacy ledger schema drift")
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
	hasChecksum, err := trackerHasChecksum(ctx, tx)
	if err != nil {
		return err
	}
	if !hasChecksum {
		return fmt.Errorf("destructive ledger rollback requires checksum-managed migrations")
	}
	for _, migration := range ordered {
		upSQL, err := files.ReadFile(migration.up)
		if err != nil {
			return fmt.Errorf("read ledger migration %s: %w", migration.up, err)
		}
		var storedChecksum string
		err = tx.QueryRow(ctx,
			`SELECT checksum FROM ledger.schema_migration WHERE version = $1`, migration.up,
		).Scan(&storedChecksum)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read ledger migration %s: %w", migration.up, err)
		}
		if storedChecksum != migrationChecksum(upSQL) {
			return fmt.Errorf("ledger migration %s checksum mismatch", migration.up)
		}
	}
	for index := len(ordered) - 1; index >= 0; index-- {
		migration := ordered[index]
		var applied bool
		err = tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM ledger.schema_migration WHERE version = $1)`, migration.up,
		).Scan(&applied)
		if err != nil {
			return fmt.Errorf("read ledger migration %s: %w", migration.up, err)
		}
		if !applied {
			continue
		}
		downSQL, err := files.ReadFile(migration.down)
		if err != nil {
			return fmt.Errorf("read ledger rollback %s: %w", migration.down, err)
		}
		if _, err := tx.Exec(ctx, string(downSQL)); err != nil {
			return fmt.Errorf("apply destructive ledger rollback %s: %w", migration.down, err)
		}
	}
	if _, err := tx.Exec(ctx, `DROP SCHEMA IF EXISTS ledger`); err != nil {
		return fmt.Errorf("remove empty ledger schema: %w", err)
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
