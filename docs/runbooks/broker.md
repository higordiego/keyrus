# Runbook: RabbitMQ / broker degraded or unreachable

Triggered by: `OutboxOldestPendingTooOld`, `RecoveryPipelineErrorsIncreasing`, `CriticalInfrastructureComponentDown` (component=infrastructure, job=rabbitmq or keycloak).

## Diagnosis

1. Check the broker is actually up:
   ```sh
   docker compose ps rabbitmq
   curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=up{job="rabbitmq"}'
   ```
2. If `up == 0`, check the container logs and management UI:
   ```sh
   docker compose logs --tail=100 rabbitmq
   open http://localhost:15672   # cashflow / $RABBITMQ_PASSWORD
   ```
3. If `up == 1` but `OutboxOldestPendingTooOld` is firing, the broker itself is healthy but the *publisher* is not draining `ledger.outbox_event`. Check:
   ```sh
   docker compose logs --tail=100 ledger-outbox-publisher
   curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=outbox_publish_errors_total'
   ```
   A rising `outbox_publish_errors_total` with a stable `outbox_pending` means publish attempts are failing (bad credentials, topology mismatch, TLS handshake failure) rather than the queue being empty of consumers.
4. For `CriticalInfrastructureComponentDown{job="keycloak"}`: Keycloak down blocks every new token issuance and JWKS refresh; existing tokens keep working until they expire (short-lived, see ADR-004). Check `docker compose logs --tail=100 keycloak` for a DB connection or realm-import failure.

## Impact

- Ledger keeps accepting and durably committing entries (the outbox pattern, ADR-006, exists precisely so a broker outage never blocks writes) -- but the Consolidated projection stops receiving new events until the publisher recovers.
- A Keycloak outage does not revoke already-issued tokens, but blocks new logins/token refreshes and any client-credentials flow the reconciliation-worker's protected `dlq` command depends on (see [reconciliation.md](reconciliation.md)).

## Safe action

- **RabbitMQ container down**: restart it (`docker compose restart rabbitmq`). Quorum queues (see [`deploy/rabbitmq/README.md`](../../deploy/rabbitmq/README.md)) survive a restart with their data intact as long as the `rabbitmq-data` volume is not removed.
- **Publisher failing to connect/publish**: verify `OUTBOX_RABBITMQ_URL`/`OUTBOX_RABBITMQ_ALLOW_INSECURE` (or the TLS material in production) match the broker's actual credentials; a credential rotation on one side without the other is the most common cause.
- **Never** manually purge `ledger.outbox_event` rows or the RabbitMQ queues to "clear" the alert -- that discards durably-committed financial events. Fix the connectivity/credential issue and let the publisher drain the backlog.

## Reprocessing

The outbox publisher retries automatically (`outbox_publish_attempts_total` increments per confirmed event) and requires no manual reprocessing once connectivity is restored. If a specific event appears stuck past several retry cycles, correlate its `event_id` from `ledger.outbox_event` with the publisher logs before escalating -- do not delete the row.

## Numeric validation

```sh
curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=outbox_pending'
curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=outbox_oldest_age_seconds'
curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=up{job=~"rabbitmq|keycloak"}'
```

## Closing condition

`outbox_oldest_age_seconds <= 30` for 2 consecutive minutes, `up{job=~"rabbitmq|keycloak"} == 1` for 2 consecutive minutes, and no further increase in `outbox_publish_errors_total`/`consolidation_consumer_retry_total`/`reconciliation_errors_total` for 10 minutes -- matching the alerts' own `closing_condition` annotations exactly.
