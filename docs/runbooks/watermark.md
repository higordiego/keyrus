# Runbook: Ledger watermark not verifiable

Triggered by: `LedgerWatermarkNotVerifiable` (`reconciliation_seconds_since_last_run < 0` or `> 900`).

This is not "the Ledger and Consolidado disagree" -- that is `DeadLetterQueueNotEmpty` / `MerchantPositionGapPersisting` firing instead, with an actual divergence measured. This alert means the reconciliation-worker itself has stopped producing *proof* one way or the other: no evidence of convergence, and none of divergence either.

## Diagnosis

1. Confirm the worker process is actually running and not crash-looping:
   ```sh
   docker compose ps reconciliation-worker
   docker compose logs --tail=100 reconciliation-worker
   curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=up{job="reconciliation-worker"}'
   ```
2. If the process is up but `reconciliation_seconds_since_last_run` keeps climbing, check what its last attempted run actually failed on:
   ```sh
   curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=reconciliation_errors_total'
   ```
   A climbing `reconciliation_errors_total` alongside a climbing `seconds_since_last_run` means every attempt is failing -- see `streamSourceEntries` in `services/consolidation/internal/reconciliation/worker.go`: after `maxStreamAttempts` (3) failed stream re-opens, `Reconcile` returns an error and persists nothing.
3. Most likely causes, in order of frequency:
   - gRPC connectivity to the Ledger is broken (mTLS certificate expiry, `CASHFLOW_LEDGER_GRPC_TARGET` unreachable, Ledger itself down).
   - The `cashflow-reconciliation-svc` client credentials are invalid or the client secret file (`CASHFLOW_SERVICE_CLIENT_SECRET_FILE`) is stale/rotated on one side only.
   - The Ledger's `ops:reconcile`/`ledger:internal:read` scope grant for that client was removed from the realm (`deploy/identity/keycloak/realm-cashflow.json`).

## Impact

There is no recent proof the Ledger (source) and the Consolidated projection agree. This does **not** mean they have diverged -- only that nobody has checked recently. Treat it as "unknown", not "known-bad", but escalate with the same urgency: an undetected divergence during this window would also go unnoticed.

## Safe action

- Restore gRPC/credential connectivity per the diagnosis above; the worker's daemon loop (a 10-second ticker, see `runDaemon` in `services/consolidation/cmd/reconciliation-worker/main.go`) resumes automatically once it can reach the Ledger again -- no manual restart of the reconciliation logic itself is needed, only of whatever dependency was broken.
- If the worker container itself is down, restart it: `docker compose restart reconciliation-worker`.

## Reprocessing

Not applicable directly -- this alert is about the *prover* being unavailable, not about a proven divergence. Once connectivity is restored and a run completes, check its actual result (see [reconciliation.md](reconciliation.md)); if it reports non-zero missing/extra/duplicated entries, follow [dlq.md](dlq.md) or [gap.md](gap.md) as appropriate for what it found.

## Numeric validation

```sh
curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=reconciliation_seconds_since_last_run'
curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=reconciliation_runs_total'
curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=reconciliation_errors_total'
```

## Closing condition

`reconciliation_seconds_since_last_run` drops back under 900 **and** `reconciliation_errors_total` stops increasing for 15 minutes -- a single lucky successful run right after a long outage is not enough on its own to declare this closed.
