package postgres

import (
	"context"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/higordiegoti/keyrus/internal/postgrestest"
	ledgermigrations "github.com/higordiegoti/keyrus/services/ledger/migrations"
)

// TestPostgresRoleIsolationAcrossLedgerAndConsolidationSchemas is the
// negative test T10 requires: on a single PostgreSQL cluster shared by both
// services -- exactly the topology docker-compose.yaml uses -- the
// schema-scoped GRANTs each service's 000004_roles migration creates must
// be a real, database-enforced barrier, not just an application-level
// convention a bug or an injected query could bypass.
//
// This runs against its own dedicated database (not the package's shared
// integrationPool/integrationDatabase) because it is the one test in this
// suite that needs the Ledger's schema applied alongside Consolidation's --
// every other test in this package only ever touches consolidation.*.
func TestPostgresRoleIsolationAcrossLedgerAndConsolidationSchemas(t *testing.T) {
	ctx := context.Background()
	database, err := postgrestest.Start(ctx, "role_isolation")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Errorf("close role isolation database: %v", err)
		}
	}()

	if err := ledgermigrations.Apply(ctx, database.Pool); err != nil {
		t.Fatalf("apply ledger migrations: %v", err)
	}
	if err := ApplyMigrations(ctx, database.Pool); err != nil {
		t.Fatalf("apply consolidation migrations: %v", err)
	}

	t.Run("ledger_app cannot read or write the consolidation schema", func(t *testing.T) {
		pool := connectAsRole(t, database.DSN, "ledger_app", "ledger_secret")
		defer pool.Close()

		if _, err := pool.Exec(ctx, `SELECT 1 FROM consolidation.daily_balance LIMIT 1`); err == nil {
			t.Fatal("ledger_app was able to SELECT from consolidation.daily_balance; schema isolation is broken")
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO consolidation.reconciliation_run (
				merchant_id, business_date, source_position_cut, missing_entries,
				extra_entries, duplicated_entries, financial_difference_minor,
				started_at, completed_at, duration_ms
			) VALUES (gen_random_uuid(), CURRENT_DATE, 1, 0, 0, 0, 0, now(), now(), 0)
		`); err == nil {
			t.Fatal("ledger_app was able to INSERT into consolidation.reconciliation_run; schema isolation is broken")
		}
	})

	t.Run("consolidation_app cannot read or write the ledger schema", func(t *testing.T) {
		pool := connectAsRole(t, database.DSN, "consolidation_app", "consolidation_secret")
		defer pool.Close()

		if _, err := pool.Exec(ctx, `SELECT 1 FROM ledger.ledger_entry LIMIT 1`); err == nil {
			t.Fatal("consolidation_app was able to SELECT from ledger.ledger_entry; schema isolation is broken")
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO ledger.merchant_position (merchant_id, last_position, updated_at)
			VALUES (gen_random_uuid(), 1, now())
		`); err == nil {
			t.Fatal("consolidation_app was able to INSERT into ledger.merchant_position; schema isolation is broken")
		}
	})
}

// connectAsRole opens a pool to the given database, authenticating as the
// named application role instead of whatever admin credentials baseDSN
// carries.
func connectAsRole(t *testing.T, baseDSN, username, password string) *pgxpool.Pool {
	t.Helper()
	dsn, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	dsn.User = url.UserPassword(username, password)
	pool, err := pgxpool.New(context.Background(), dsn.String())
	if err != nil {
		t.Fatalf("connect as %s: %v", username, err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping as %s: %v", username, err)
	}
	return pool
}
