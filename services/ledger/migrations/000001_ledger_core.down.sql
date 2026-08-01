DROP TABLE IF EXISTS ledger.outbox_event;
DROP TABLE IF EXISTS ledger.idempotency_record;
DROP TRIGGER IF EXISTS ledger_entry_immutable ON ledger.ledger_entry;
DROP FUNCTION IF EXISTS ledger.reject_ledger_entry_mutation();
DROP TABLE IF EXISTS ledger.ledger_entry;
DROP TABLE IF EXISTS ledger.merchant_position;
DROP TABLE IF EXISTS ledger.schema_migration;
