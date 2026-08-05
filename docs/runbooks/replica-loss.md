# Runbook: Replica loss (KrakenD, service instances, RabbitMQ quorum member)

Covers losing a redundant instance of a horizontally-scaled component -- not a total outage of the component (see [broker.md](broker.md) for that) and not application data loss.

## KrakenD gateway

Per [ADR-011](../adrs/ADR-011-krakend-gateway.md), KrakenD is stateless and designed to scale horizontally; the ADR itself names "all KrakenD replicas down" as the one real single point of failure this architecture accepts. Losing *one* replica out of several behind a load balancer should be invisible to merchants -- the remaining replicas keep serving.

**Diagnosis**: check replica count and health at whatever layer runs multiple KrakenD instances (this Compose stack runs exactly one `krakend` service/container, so today "replica loss" for KrakenD in this environment means the single instance is down -- there is no redundancy to fall back to until a multi-replica deployment target, e.g. Swarm with `replicas: N`, is in place; see T10).

**Safe action**: restart the failed replica/container; if using a load balancer, confirm it deregistered the unhealthy instance before it recovers (avoid sending traffic to a still-starting instance).

**Closing condition**: replica count back to the configured target, `krakend`'s own health check passing.

## Ledger/Consolidation API replicas

Both `ledger-api` and `consolidation-api` are stateless HTTP/gRPC services reading/writing Postgres; losing one instance behind a load balancer is the same story as KrakenD -- the remaining instances keep serving as long as they are not all pinned to the same failure domain.

**Diagnosis**:
```sh
curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=up{job=~"ledger-api|consolidation-api"}'
```

**Safe action**: restart the failed instance; do not restart every instance simultaneously (loses all capacity at once). Confirm Postgres connection pool limits (`max_connections`) are not being hit by the surviving instances absorbing the lost instance's load.

**Closing condition**: `up == 1` for every expected instance, `cashflow_http_failures_total` rate back to baseline.

## RabbitMQ quorum queue member loss

The active Consolidation queue (`consolidation.ledger-entry-confirmed.v2`, see [`deploy/rabbitmq/README.md`](../../deploy/rabbitmq/README.md)) is a durable quorum queue. Losing one member of a multi-node quorum (a Swarm/multi-broker deployment, not this single-node Compose stack) does not lose data as long as a quorum of the remaining members is intact.

**Diagnosis**: RabbitMQ management UI (`http://localhost:15672`) or `rabbitmq-diagnostics check_running`/`cluster_status` inside the container.

**Safe action**: bring the lost node back and let it rejoin the cluster; RabbitMQ resynchronizes the quorum queue automatically. Do not force-delete and recreate the queue to "fix" a degraded quorum -- that discards whatever the surviving members had.

**Closing condition**: all expected cluster members reporting `running`, quorum queue leader/followers count back to the configured replication factor.

## What this runbook does not cover

Total loss of the broker/DB/gateway (not just one replica) -- see [broker.md](broker.md) and [watermark.md](watermark.md). Data-level divergence after a replica rejoins -- see [reconciliation.md](reconciliation.md).
