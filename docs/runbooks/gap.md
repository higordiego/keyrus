# Runbook: Merchant position gap persisting

Triggered by: `MerchantPositionGapPersisting` (`consolidation_consumer_gaps > 0`, sustained 30s).

## Diagnosis

1. Identify the affected merchant(s) and the missing position:
   ```sql
   SELECT merchant_id, source_position, applied_position, first_gap, gap_detected_at
   FROM consolidation.merchant_progress
   WHERE source_position > applied_position;
   ```
   `first_gap` is the earliest missing position blocking `applied_position` from advancing (see `services/consolidation/internal/adapters/outbound/postgres/store.go`).
2. Find where that specific position currently is:
   ```sql
   SELECT event_id, failure_class, error_code, attempts, last_failed_at
   FROM consolidation.event_pending
   WHERE merchant_id = $1;
   ```
   - `failure_class = 'retry'`: still retrying, will self-heal once the transient cause clears.
   - `failure_class = 'dlq'`: isolated permanently until reprocessed -- go to [dlq.md](dlq.md).
   - No row at all: the event may still be in flight through RabbitMQ, or was never published (check [broker.md](broker.md) and the Ledger outbox for that merchant/position).

## Impact

The merchant's balance is correct up to `applied_position - 1` and will not include anything at or after the gap until it closes -- out-of-order deliveries at higher positions are held, not discarded (see `manter-lancamentos-durante-falha.feature`'s ordering scenarios).

## Safe action

- If the gap is in `retry`, no action is usually needed; confirm `attempts`/`last_failed_at` are still advancing and the retry backoff has not stalled.
- If in `dlq`, follow [dlq.md](dlq.md) -- do not try to "skip" the position by fabricating a synthetic entry.
- If the event was never published at all, check the Ledger's own outbox for that merchant/position (`ledger.outbox_event`) and [broker.md](broker.md).

## Reprocessing

Reprocessing (via the DLQ path or once the retry succeeds) applies the missing position; `consolidation_consumer_gaps` clears automatically once `applied_position` catches up to `source_position` -- there is no separate "close the gap" step beyond resolving whatever is blocking that one position.

## Numeric validation

```sh
curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=consolidation_consumer_gaps'
```
```sql
SELECT merchant_id, source_position, applied_position FROM consolidation.merchant_progress
WHERE merchant_id = $1;
-- applied_position must equal source_position once resolved
```

## Closing condition

`consolidation_consumer_gaps == 0`.
