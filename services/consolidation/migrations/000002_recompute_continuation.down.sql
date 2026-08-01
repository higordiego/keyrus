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

-- Restoring the original bound deliberately fails if durable long-running
-- continuations still exist; operators must finish or archive them first.
ALTER TABLE consolidation.recompute_job
    ADD CONSTRAINT recompute_job_date_span_check
    CHECK (through_date - from_date <= 30);
