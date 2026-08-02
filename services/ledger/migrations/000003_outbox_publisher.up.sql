ALTER TABLE ledger.outbox_event
    ADD COLUMN lease_owner text NULL,
    ADD COLUMN lease_until timestamptz NULL,
    ADD CONSTRAINT outbox_event_lease_consistent CHECK (
        (lease_owner IS NULL AND lease_until IS NULL)
        OR
        (lease_owner IS NOT NULL AND lease_until IS NOT NULL)
    );

CREATE INDEX outbox_event_claim_idx
    ON ledger.outbox_event (available_at, lease_until, created_at)
    WHERE published_at IS NULL;
