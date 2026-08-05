# Runbook: Cache (Redis) unavailable

## Current status: no cache is deployed

This runbook is written ahead of the cache layer T07 was scoped to build, because a runbook that only gets written *after* the first incident is a runbook that arrives too late. As of this session, `services/consolidation` has no Redis client, no `redis` service in `docker-compose.yaml`, and no cache-related configuration anywhere in the codebase -- `docs/compliance-matrix.md` and the T09 dashboard's "Cache" panel both say so explicitly rather than pretending otherwise.

Do not action an alert or dashboard panel that appears to reference a cache metric (`redis_*`, `cache_hit_ratio`, etc.) -- none of those metrics are wired to anything real yet; if one appears, it is a regression in the observability config, not a cache incident.

## What this runbook will cover once a cache exists

`@SCN-RNF07-006` ("Consultar durante indisponibilidade do cache") already specifies the required behavior: a balance query must return the same values and positions from the Consolidated projection's own persistence when the cache is unavailable, the query must never treat the cache as a mandatory readiness dependency, and a fallback metric must increment. When that scenario has a real binding (it does not yet -- see `features/recuperar-erros-sem-saldo-incorreto.feature` and the BDD manifest), this runbook should be filled in with:

1. How to confirm the cache is actually down (`up{job="redis"} == 0` or equivalent) versus the fallback path simply being exercised under normal cache eviction.
2. Whether the fallback rate (`cache miss` / `cache fallback to PostgreSQL`, per the technical plan's minimum metrics table) is within the expected range for current traffic, or high enough to indicate the cache itself needs attention even though correctness is unaffected.
3. The safe action for a degraded-but-not-down cache (e.g. eviction storm, memory pressure) versus a fully unreachable one.
4. That no action is required to protect correctness during the outage -- RNF07-006 guarantees Postgres is authoritative and always answers -- only to restore the performance the cache exists for.

## Closing condition (once implemented)

Cache reachable again, `cache_fallback_total` rate back to baseline, `up{job="redis"} == 1` for 2 consecutive minutes.
