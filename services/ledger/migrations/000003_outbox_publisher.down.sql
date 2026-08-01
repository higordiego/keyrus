DROP INDEX IF EXISTS ledger.outbox_event_claim_idx;

ALTER TABLE ledger.outbox_event
    DROP CONSTRAINT IF EXISTS outbox_event_lease_consistent,
    DROP COLUMN IF EXISTS lease_until,
    DROP COLUMN IF EXISTS lease_owner;
