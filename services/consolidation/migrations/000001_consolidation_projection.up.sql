CREATE SCHEMA IF NOT EXISTS consolidation;

CREATE TABLE consolidation.inbox_event (
    event_id UUID PRIMARY KEY,
    event_type TEXT NOT NULL CHECK (event_type = 'ledger.entry.confirmed.v1'),
    payload_fingerprint CHAR(64) NOT NULL CHECK (payload_fingerprint ~ '^[0-9a-f]{64}$'),
    merchant_id UUID NOT NULL,
    position BIGINT NOT NULL CHECK (position > 0),
    entry_id UUID NOT NULL,
    entry_type TEXT NOT NULL CHECK (entry_type IN ('credit', 'debit')),
    amount_minor BIGINT NOT NULL CHECK (amount_minor > 0),
    currency CHAR(3) NOT NULL CHECK (currency = 'BRL'),
    business_date DATE NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    confirmed_at TIMESTAMPTZ NOT NULL,
    original_entry_id UUID NULL,
    traceparent TEXT NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (merchant_id, position)
);

CREATE INDEX inbox_event_merchant_date_idx
    ON consolidation.inbox_event (merchant_id, business_date, position);

CREATE TABLE consolidation.daily_balance (
    merchant_id UUID NOT NULL,
    business_date DATE NOT NULL,
    credits_minor BIGINT NOT NULL DEFAULT 0 CHECK (credits_minor >= 0),
    debits_minor BIGINT NOT NULL DEFAULT 0 CHECK (debits_minor >= 0),
    net_minor BIGINT NOT NULL DEFAULT 0,
    entry_count BIGINT NOT NULL DEFAULT 0 CHECK (entry_count >= 0),
    closing_balance_minor BIGINT NOT NULL DEFAULT 0,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (merchant_id, business_date),
    CHECK (net_minor = credits_minor - debits_minor)
);

CREATE TABLE consolidation.merchant_progress (
    merchant_id UUID PRIMARY KEY,
    source_position BIGINT NOT NULL DEFAULT 0 CHECK (source_position >= 0),
    applied_position BIGINT NOT NULL DEFAULT 0 CHECK (applied_position >= 0),
    first_gap BIGINT NULL CHECK (first_gap > 0),
    gap_detected_at TIMESTAMPTZ NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    CHECK (applied_position <= source_position),
    CHECK (
        (first_gap IS NULL AND applied_position = source_position)
        OR first_gap = applied_position + 1
    )
);

CREATE TABLE consolidation.position_receipt (
    merchant_id UUID NOT NULL,
    position BIGINT NOT NULL CHECK (position > 0),
    event_id UUID NOT NULL UNIQUE,
    received_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (merchant_id, position)
);

CREATE TABLE consolidation.recompute_job (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id UUID NOT NULL,
    merchant_id UUID NOT NULL,
    from_date DATE NOT NULL,
    through_date DATE NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed')),
    attempts INTEGER NOT NULL DEFAULT 1 CHECK (attempts > 0),
    last_error_code TEXT NULL,
    started_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    completed_at TIMESTAMPTZ NULL,
    CHECK (through_date >= from_date),
    CHECK (through_date - from_date <= 30),
    CHECK ((status = 'completed') = (completed_at IS NOT NULL))
);

CREATE INDEX recompute_job_merchant_status_idx
    ON consolidation.recompute_job (merchant_id, status, from_date, through_date);

-- The transport ticket owns retry/ACK behavior. These tables preserve the audit
-- state its adapter will write without making broker concerns part of the projector.
CREATE TABLE consolidation.event_pending (
    event_id UUID PRIMARY KEY,
    merchant_id UUID NULL,
    business_date DATE NULL,
    failure_class TEXT NOT NULL CHECK (failure_class IN ('retry', 'dlq')),
    attempts INTEGER NOT NULL DEFAULT 1 CHECK (attempts > 0),
    first_failed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    last_failed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    next_attempt_at TIMESTAMPTZ NULL,
    error_code TEXT NOT NULL,
    CHECK (last_failed_at >= first_failed_at)
);

CREATE INDEX event_pending_merchant_date_idx
    ON consolidation.event_pending (merchant_id, business_date, last_failed_at);

CREATE TABLE consolidation.dead_letter_event (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id UUID NULL,
    merchant_id UUID NULL,
    business_date DATE NULL,
    event_type TEXT NULL,
    payload JSONB NOT NULL,
    error_code TEXT NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX dead_letter_event_merchant_date_idx
    ON consolidation.dead_letter_event (merchant_id, business_date, recorded_at);
