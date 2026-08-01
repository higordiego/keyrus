CREATE SCHEMA IF NOT EXISTS ledger;

CREATE TABLE ledger.merchant_position (
    merchant_id uuid PRIMARY KEY,
    last_position bigint NOT NULL CHECK (last_position >= 0),
    updated_at timestamptz NOT NULL
);

CREATE TABLE ledger.ledger_entry (
    id uuid PRIMARY KEY,
    merchant_id uuid NOT NULL,
    position bigint NOT NULL CHECK (position > 0),
    entry_type text NOT NULL CHECK (entry_type IN ('credit', 'debit')),
    amount_minor bigint NOT NULL CHECK (amount_minor > 0),
    currency character(3) NOT NULL CHECK (currency = 'BRL'),
    business_date date NOT NULL,
    description text NULL CHECK (description IS NULL OR char_length(description) <= 500),
    confirmed_at timestamptz NOT NULL,
    original_entry_id uuid NULL,
    CONSTRAINT ledger_entry_merchant_position_unique UNIQUE (merchant_id, position),
    CONSTRAINT ledger_entry_merchant_id_unique UNIQUE (merchant_id, id),
    CONSTRAINT ledger_entry_original_not_self CHECK (original_entry_id IS NULL OR original_entry_id <> id),
    CONSTRAINT ledger_entry_original_same_merchant_fk
        FOREIGN KEY (merchant_id, original_entry_id)
        REFERENCES ledger.ledger_entry (merchant_id, id)
);

CREATE UNIQUE INDEX ledger_entry_one_reversal_per_original
    ON ledger.ledger_entry (original_entry_id)
    WHERE original_entry_id IS NOT NULL;
CREATE INDEX ledger_entry_merchant_history_idx
    ON ledger.ledger_entry (merchant_id, business_date DESC, confirmed_at DESC, id DESC);
CREATE INDEX ledger_entry_merchant_confirmation_idx
    ON ledger.ledger_entry (merchant_id, confirmed_at DESC, id DESC);

CREATE OR REPLACE FUNCTION ledger.reject_ledger_entry_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'ledger entries are immutable' USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER ledger_entry_immutable
BEFORE UPDATE OR DELETE ON ledger.ledger_entry
FOR EACH ROW EXECUTE FUNCTION ledger.reject_ledger_entry_mutation();

CREATE TABLE ledger.idempotency_record (
    attempt_id uuid PRIMARY KEY,
    merchant_id uuid NOT NULL,
    operation text NOT NULL CHECK (operation IN ('create_entry', 'reverse_entry')),
    key_hash bytea NOT NULL CHECK (octet_length(key_hash) = 32),
    request_hash bytea NOT NULL CHECK (octet_length(request_hash) = 32),
    entry_id uuid NULL REFERENCES ledger.ledger_entry (id),
    response_payload jsonb NULL,
    created_at timestamptz NOT NULL,
    completed_at timestamptz NULL,
    CONSTRAINT idempotency_record_scope_unique UNIQUE (merchant_id, operation, key_hash),
    CONSTRAINT idempotency_record_completion_consistent CHECK (
        (entry_id IS NULL AND response_payload IS NULL AND completed_at IS NULL)
        OR
        (entry_id IS NOT NULL AND response_payload IS NOT NULL AND completed_at IS NOT NULL)
    )
);
CREATE INDEX idempotency_record_merchant_created_idx
    ON ledger.idempotency_record (merchant_id, created_at DESC);

CREATE TABLE ledger.outbox_event (
    event_id uuid PRIMARY KEY,
    aggregate_id uuid NOT NULL REFERENCES ledger.ledger_entry (id),
    merchant_id uuid NOT NULL,
    merchant_position bigint NOT NULL CHECK (merchant_position > 0),
    event_type text NOT NULL,
    payload jsonb NOT NULL,
    occurred_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    available_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz NULL,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error text NULL,
    CONSTRAINT outbox_event_aggregate_type_unique UNIQUE (aggregate_id, event_type),
    CONSTRAINT outbox_event_merchant_position_fk
        FOREIGN KEY (merchant_id, merchant_position)
        REFERENCES ledger.ledger_entry (merchant_id, position)
);
CREATE INDEX outbox_event_pending_idx
    ON ledger.outbox_event (available_at, created_at)
    WHERE published_at IS NULL;
CREATE INDEX outbox_event_merchant_position_idx
    ON ledger.outbox_event (merchant_id, merchant_position);
