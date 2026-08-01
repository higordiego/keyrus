ALTER TABLE ledger.outbox_event
    DROP CONSTRAINT outbox_event_entry_correlation_fk,
    ADD CONSTRAINT outbox_event_aggregate_id_fkey
        FOREIGN KEY (aggregate_id)
        REFERENCES ledger.ledger_entry (id),
    ADD CONSTRAINT outbox_event_merchant_position_fk
        FOREIGN KEY (merchant_id, merchant_position)
        REFERENCES ledger.ledger_entry (merchant_id, position);

ALTER TABLE ledger.ledger_entry
    DROP CONSTRAINT ledger_entry_merchant_id_position_unique;

ALTER TABLE ledger.idempotency_record
    DROP CONSTRAINT idempotency_record_entry_same_merchant_fk,
    ADD CONSTRAINT idempotency_record_entry_id_fkey
        FOREIGN KEY (entry_id)
        REFERENCES ledger.ledger_entry (id);

ALTER TABLE ledger.schema_migration
    DROP COLUMN checksum;
