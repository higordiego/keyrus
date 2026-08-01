ALTER TABLE ledger.schema_migration
    ADD COLUMN checksum character(64);

UPDATE ledger.schema_migration
SET checksum = '7df6b8f3cc82408ae9c53e99d7e7667e55a0431bc54e8de8e8cf7238989d4417'
WHERE version = '000001_ledger_core.up.sql'
  AND checksum IS NULL;

ALTER TABLE ledger.schema_migration
    ALTER COLUMN checksum SET NOT NULL;

ALTER TABLE ledger.idempotency_record
    DROP CONSTRAINT idempotency_record_entry_id_fkey,
    ADD CONSTRAINT idempotency_record_entry_same_merchant_fk
        FOREIGN KEY (merchant_id, entry_id)
        REFERENCES ledger.ledger_entry (merchant_id, id);

ALTER TABLE ledger.ledger_entry
    ADD CONSTRAINT ledger_entry_merchant_id_position_unique
        UNIQUE (merchant_id, id, position);

ALTER TABLE ledger.outbox_event
    DROP CONSTRAINT outbox_event_aggregate_id_fkey,
    DROP CONSTRAINT outbox_event_merchant_position_fk,
    ADD CONSTRAINT outbox_event_entry_correlation_fk
        FOREIGN KEY (merchant_id, aggregate_id, merchant_position)
        REFERENCES ledger.ledger_entry (merchant_id, id, position);
