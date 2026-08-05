# Runbook: Running and interpreting reconciliation

This is the general-purpose runbook for the reconciliation-worker itself (T08): how to run it, and how to read its result. For the specific "the worker has stopped proving anything" alert, see [watermark.md](watermark.md); for "it proved a DLQ item exists", see [dlq.md](dlq.md); for a position gap, see [gap.md](gap.md).

## Running a reconciliation on demand

```sh
docker compose exec reconciliation-worker \
  reconciliation-worker run <merchant_id> <business_date>
```

This fetches the merchant's current watermark from the Ledger (`GetMerchantWatermark`), then compares the Ledger's stream up to that cut against the Consolidated projection (`services/consolidation/internal/reconciliation/worker.go`). It is safe to run repeatedly: repeating the exact same cut is a no-op (proven by `TestReconcile_RepeatSameCut_NoEffect`), and it can never overwrite a newer already-persisted result with an older one (`TestReconcile_NewerVersionNeverOverwritten`), even under concurrent execution (`TestReconcile_ConcurrentCut`).

## Reading the result

```sql
SELECT source_position_cut, missing_entries, extra_entries, duplicated_entries,
       financial_difference_minor, started_at, completed_at, duration_ms
FROM consolidation.reconciliation_run
WHERE merchant_id = $1 AND business_date = $2;
```

- **`missing_entries` > 0**: present on the Ledger, absent from the Consolidated projection -- almost always an active or resolved-but-unreconciled gap; see [gap.md](gap.md).
- **`extra_entries` > 0**: present on the Consolidated projection, absent from the Ledger at that cut. Investigate before assuming this is benign -- it can mean a delivery arrived after the cut was taken (re-run at a fresher cut) or, more seriously, a data-integrity issue in the projection.
- **`duplicated_entries` > 0**: the same `entry_id` appears more than once in `consolidation.inbox_event` for that merchant/date -- a projector invariant that should be impossible in normal operation (`UNIQUE (merchant_id, position)` plus the conflict checks in `services/consolidation/internal/adapters/outbound/postgres/store.go`); treat any non-zero value as a bug report, not routine noise.
- **`financial_difference_minor` > 0**: the absolute difference between the Ledger's and the projection's net (credits − debits) at the cut, in minor currency units. This can be non-zero even when the three entry counts above are all zero if amounts were recorded incorrectly for a matching entry -- always check this figure even when the counts look clean.

None of these fields ever carry the entry's description or amount by itself (only aggregated diffs), so this table is safe to query and log without redaction (see [ADR-010](../adrs/ADR-010-log-redaction.md)).

## Reprocessing the DLQ

See [dlq.md](dlq.md) for the protected `reconciliation-worker dlq` command and its audit trail (`consolidation.dlq_reprocess_audit`).

## Numeric validation

The condition of success for any reprocessing is **exactly one** financial effect per item -- not "at most one". After reprocessing, re-run reconciliation at a cut that includes the reprocessed item and confirm all four divergence fields read zero.

## Closing condition

`missing_entries = extra_entries = duplicated_entries = 0` and `financial_difference_minor = 0` for the run at the relevant cut.
