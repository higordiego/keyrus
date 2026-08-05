# Runbook: Dead Letter Queue not empty

Triggered by: `DeadLetterQueueNotEmpty` (`consolidation_consumer_pending_dlq > 0`).

## Diagnosis

1. Find what's in the DLQ and why:
   ```sql
   SELECT event_id, merchant_id, business_date, error_code, recorded_at
   FROM consolidation.dead_letter_event
   ORDER BY recorded_at DESC;
   ```
   `error_code` comes from `application.ClassifyFailure` (services/consolidation/internal/application/projector.go): anything landing here failed validation or hit a persisted conflict, not a transient error -- a transient error would have stayed in `retry` (`event_pending.failure_class = 'retry'`), which never reaches this table.
2. Correlate with `trace_id` in the consumer logs to see the original payload and the exact validation error (never log the payload itself -- see [ADR-010](../adrs/ADR-010-log-redaction.md)).
3. Common root causes: a payload that fails `domain.EntryConfirmed.Validate()` (bad UUID, unsupported `event_type`, non-BRL currency, missing `original_entry_id`), or a genuine identity conflict (`ConflictError`: `event_id`/`entry_id`/position reused with different content).

## Impact

The affected merchant's balance for the item's `business_date` stops advancing until the item is resolved -- indefinitely, not just until a retry budget expires. `consolidation_consumer_gaps` may also be non-zero for the same merchant if downstream positions are waiting on this one (see [gap.md](gap.md)).

## Safe action

1. **Fix the root cause first.** If the payload is malformed because of a producer bug, fix and deploy the producer before reprocessing -- reprocessing an item that will fail again just re-DLQs it.
2. **Never** hand-edit `dead_letter_event.payload` or `INSERT`/`UPDATE` `consolidation.*` tables directly to "fix" the balance. That bypasses every invariant the projector enforces (see `services/consolidation/internal/application/projector.go`) and cannot be distinguished later from a real financial event.
3. Reprocess through the protected command, never a manual query:
   ```sh
   TOKEN=$(curl -s -X POST "$CASHFLOW_OIDC_TOKEN_URL" \
     --cacert "$CASHFLOW_OIDC_CA_FILE" \
     -d grant_type=client_credentials \
     -d client_id=cashflow-reconciliation-svc \
     -d client_secret="$(cat /run/secrets/reconciliation-client-secret)" \
     | jq -r .access_token)
   docker compose exec -e CASHFLOW_OPERATOR_TOKEN="$TOKEN" reconciliation-worker \
     reconciliation-worker dlq
   ```
   This requires a bearer token whose identity carries `ops:reconcile` (`auth.ScopeOpsReconcile`), verified against the same OIDC issuer/JWKS every other surface trusts (see `runDLQCommand` in `services/consolidation/cmd/reconciliation-worker/main.go` and [ADR-008](../adrs/ADR-008-reconciliation-worker.md)). It is audited: every run inserts exactly one row into `consolidation.dlq_reprocess_audit` recording the verified token subject as `actor`, `requested_at`/`completed_at`, and `reprocessed_count`/`failed_count` -- regardless of outcome, including a run that reprocesses zero items.
   **Known gap:** the realm currently only provisions the `cashflow-reconciliation-svc` *service* client for this scope (see `deploy/identity/keycloak/realm-cashflow.json`) -- there is no separate human-operator identity yet, so the audit `actor` today is that service account's subject, not an individual operator. Provisioning a per-operator client (or federating to an SSO identity) with the same `ops:reconcile` scope is tracked separately, not done as part of T08/T09.
4. Check the audit trail:
   ```sql
   SELECT actor, requested_at, completed_at, reprocessed_count, failed_count, outcome
   FROM consolidation.dlq_reprocess_audit
   ORDER BY requested_at DESC LIMIT 5;
   ```
   `outcome = 'partial_failure'` means at least one item is still failing and remains in the DLQ for the next attempt -- it is never silently dropped.

## Numeric validation

Every reprocessed item must produce **exactly one** financial effect -- not zero (silently dropped) and not more than one (double-applied):
```sql
-- Before/after balance for the affected merchant/date must differ by exactly
-- the reprocessed item's amount, once.
SELECT credits_minor, debits_minor, entry_count FROM consolidation.daily_balance
WHERE merchant_id = $1 AND business_date = $2;
```
Then run the reconciliation worker for the affected merchant/date at the post-reprocessing cut and confirm the divergence is zero:
```sh
docker compose exec reconciliation-worker reconciliation-worker run <merchant_id> <business_date>
curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=reconciliation_last_run_missing_entries'
curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=reconciliation_last_run_extra_entries'
curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=reconciliation_last_run_duplicated_entries'
```

## Closing condition

`consolidation_consumer_pending_dlq == 0` **and** the reconciliation run for the affected merchant/date at the post-reprocessing cut reports zero missing, extra and duplicated entries -- checking DLQ depth alone is not sufficient, because a reprocessed-but-still-diverging item would clear the DLQ without actually being correct.
