CREATE TABLE consolidation.dlq_reprocess_audit (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor TEXT NOT NULL,
    requested_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ NOT NULL,
    reprocessed_count BIGINT NOT NULL CHECK (reprocessed_count >= 0),
    failed_count BIGINT NOT NULL CHECK (failed_count >= 0),
    outcome TEXT NOT NULL
);

CREATE INDEX idx_dlq_reprocess_audit_requested_at
    ON consolidation.dlq_reprocess_audit (requested_at DESC);

GRANT SELECT, INSERT ON consolidation.dlq_reprocess_audit TO consolidation_app;
