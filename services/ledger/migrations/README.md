# Ledger migrations

`Apply` is the forward-only production path. It records a SHA-256 checksum for
every applied migration and refuses to continue when an already-applied file no
longer matches its recorded checksum.

`000001_ledger_core` is the immutable migration published in `37596ee`.
`000002_ledger_integrity` adopts its historical checksum, upgrades the
tenant-aware references without recreating tables, and correlates each outbox
aggregate ID with the position of that same entry. A legacy database is accepted
only when its tracker and critical `000001` constraints match the trusted state.

`RollbackAll` and the `*.down.sql` files are **destructive**: they remove the
entire `ledger` schema, including financial entries, idempotency responses and
outbox events. They are restricted to disposable development databases or an
explicitly approved recovery procedure with a verified backup. They are not a
normal production rollback mechanism.

To roll back only the application binary, leave the database at `000002`. The
schema remains compatible with the `37596ee` application because the upgrade
only strengthens constraints and adds migration metadata. Do not run
`RollbackAll`: an application rollback never requires deleting Ledger data.

## Minimum Ledger runtime grants

The API runtime uses a non-superuser role. Replace `ledger_runtime` with the
deployed role name:

```sql
GRANT USAGE ON SCHEMA ledger TO ledger_runtime;
GRANT SELECT, INSERT, UPDATE ON ledger.merchant_position TO ledger_runtime;
GRANT SELECT, INSERT ON ledger.ledger_entry TO ledger_runtime;
GRANT UPDATE (id) ON ledger.ledger_entry TO ledger_runtime;
GRANT SELECT, INSERT, UPDATE ON ledger.idempotency_record TO ledger_runtime;
GRANT SELECT, INSERT ON ledger.outbox_event TO ledger_runtime;
```

The column-level `UPDATE (id)` privilege is needed by PostgreSQL for
`SELECT ... FOR UPDATE` during reversal serialization. The immutable-row trigger
still rejects every actual `UPDATE` or `DELETE` against `ledger_entry`. Publisher
grants are separate and intentionally outside T03A.
