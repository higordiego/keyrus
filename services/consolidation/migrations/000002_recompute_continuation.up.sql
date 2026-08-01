ALTER TABLE consolidation.recompute_job
    DROP CONSTRAINT recompute_job_status_check;

ALTER TABLE consolidation.recompute_job
    ADD CONSTRAINT recompute_job_status_check
    CHECK (status IN ('pending', 'running', 'completed', 'failed'));

ALTER TABLE consolidation.recompute_job
    ADD COLUMN next_date DATE NULL;

DO $migration$
DECLARE
    bounded_constraint RECORD;
BEGIN
    FOR bounded_constraint IN
        SELECT conname
        FROM pg_constraint
        WHERE conrelid = 'consolidation.recompute_job'::regclass
          AND contype = 'c'
          AND pg_get_constraintdef(oid) LIKE '%through_date%from_date%30%'
    LOOP
        EXECUTE format(
            'ALTER TABLE consolidation.recompute_job DROP CONSTRAINT %I',
            bounded_constraint.conname
        );
    END LOOP;
END
$migration$;

ALTER TABLE consolidation.recompute_job
    ADD CONSTRAINT recompute_job_continuation_check
    CHECK (
        (status IN ('pending', 'running') AND next_date BETWEEN from_date AND through_date)
        OR (status IN ('completed', 'failed') AND next_date IS NULL)
    );

CREATE INDEX recompute_job_merchant_next_idx
    ON consolidation.recompute_job (merchant_id, next_date, id)
    WHERE status = 'pending';
