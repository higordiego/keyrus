-- Preflight is intentionally the first statement so even clients that
-- autocommit each statement cannot leave a refused downgrade half-applied.
DO $preflight$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM consolidation.recompute_job
        WHERE through_date - from_date > 30
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '55000',
            MESSAGE = 'cannot downgrade recompute continuation while jobs span more than 31 inclusive days; archive or remove incompatible long-range jobs before retrying';
    END IF;
END
$preflight$;

DROP INDEX IF EXISTS consolidation.recompute_job_merchant_next_idx;

UPDATE consolidation.recompute_job
SET status = 'failed', next_date = NULL
WHERE status IN ('pending', 'running');

ALTER TABLE consolidation.recompute_job
    DROP CONSTRAINT IF EXISTS recompute_job_continuation_check;

ALTER TABLE consolidation.recompute_job
    DROP COLUMN IF EXISTS next_date;

ALTER TABLE consolidation.recompute_job
    DROP CONSTRAINT recompute_job_status_check;

ALTER TABLE consolidation.recompute_job
    ADD CONSTRAINT recompute_job_status_check
    CHECK (status IN ('running', 'completed', 'failed'));

ALTER TABLE consolidation.recompute_job
    ADD CONSTRAINT recompute_job_date_span_check
    CHECK (through_date - from_date <= 30);
