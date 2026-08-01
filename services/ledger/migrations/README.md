# Ledger migrations

`Apply` is the forward-only production path. It records a SHA-256 checksum for
every applied migration and refuses to continue when an already-applied file no
longer matches its recorded checksum.

`RollbackAll` and the `*.down.sql` files are **destructive**: they remove the
entire `ledger` schema, including financial entries, idempotency responses and
outbox events. They are restricted to disposable development databases or an
explicitly approved recovery procedure with a verified backup. They are not a
normal production rollback mechanism.

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
