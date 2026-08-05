# Runbooks

Each file below is linked directly from the alert that triggers it via the alert's `runbook_url` annotation (see [`deploy/observability/prometheus/rules/cashflow-alerts.yml`](../../deploy/observability/prometheus/rules/cashflow-alerts.yml)). Every runbook follows the same structure the T09 ticket requires per alert: diagnosis, impact, safe action, reprocessing (where applicable), numeric validation, and an objective closing condition -- not just a threshold.

| Runbook | Covers |
| --- | --- |
| [broker.md](broker.md) | RabbitMQ / Keycloak degraded or unreachable, outbox backlog, rising pipeline errors |
| [dlq.md](dlq.md) | Dead-lettered events: diagnosis and the protected reprocess command |
| [gap.md](gap.md) | A merchant's `source_position > applied_position` persisting |
| [redis.md](redis.md) | Cache -- not implemented yet; documents the target behavior (`@SCN-RNF07-006`) so the runbook exists ahead of the incident, not after |
| [watermark.md](watermark.md) | The reconciliation worker itself going stale (no recent proof of convergence, not a proven divergence) |
| [reconciliation.md](reconciliation.md) | Running reconciliation on demand and reading its result |
| [replica-loss.md](replica-loss.md) | Losing one redundant instance of KrakenD, an API service, or a RabbitMQ quorum member |
| [api-health.md](api-health.md) | Consolidation API error rate / p95 latency degraded (supplementary; didn't fit the seven named categories above) |
