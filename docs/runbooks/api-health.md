# Runbook: Consolidation API error rate or latency degraded

Triggered by: `ConsolidationErrorRateAboveFivePercent`, `ConsolidationP95LatencyAboveFiveHundredMillis`.

Supplementary to the seven runbooks the T09 ticket names explicitly -- general Consolidation API health didn't fit cleanly under any of "broker, DLQ, gap, Redis, watermark, reconciliação, perda de réplica", so it gets its own file rather than being force-fit into [watermark.md](watermark.md) (which is specifically about the reconciliation worker going stale, a different failure mode).

## Diagnosis

1. Correlate the error/latency spike with a trace in Jaeger (`http://localhost:16686`, service `consolidation-api`) using the `trace_id` from the affected requests' logs.
2. Check Postgres connection pool saturation and query latency -- the most common cause for both symptoms simultaneously:
   ```sh
   curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=up{job="consolidation-api"}'
   docker compose logs --tail=100 consolidation-api
   ```
3. Check container resource pressure (CPU/memory) if the deployment target exposes it (see the `known_gap` on `CriticalInfrastructureComponentDown` -- resource metrics are not yet scraped, see T10).
4. If the error rate spike coincides with a recent deploy, check whether it correlates with a specific endpoint or affects all traffic uniformly.

## Impact

Merchants see failed or slow balance queries; sustained latency above ~1s risks KrakenD's own timeout tripping (see [ADR-011](../adrs/ADR-011-krakend-gateway.md)) and turning a slow response into a hard failure.

## Safe action

Roll back a recent deploy if the timing correlates; scale out `consolidation-api` replicas if the cause is load rather than a bug (per-instance load also visible via `sum by (service) (rate(cashflow_http_requests_total[5m]))` on the dashboard); fix the underlying query/connection issue directly if that's the cause.

## Reprocessing

Not applicable -- this alert is about read-path health, not data correctness. No reconciliation or DLQ action follows from it on its own.

## Numeric validation

```sh
curl -s http://localhost:9090/api/v1/query --data-urlencode \
  'query=sum(rate(cashflow_http_failures_total{service="consolidation-api"}[5m]))/sum(rate(cashflow_http_requests_total{service="consolidation-api"}[5m]))'
curl -s http://localhost:9090/api/v1/query --data-urlencode \
  'query=histogram_quantile(0.95, sum(rate(cashflow_http_request_duration_seconds_bucket{service="consolidation-api"}[5m])) by (le))'
```

## Closing condition

Error rate <= 5% and p95 <= 500ms, both sustained for 5 consecutive minutes.
