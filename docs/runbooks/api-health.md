# Runbook: Taxa de erro ou latência da Consolidation API degradada

Acionado por: `ConsolidationErrorRateAboveFivePercent`, `ConsolidationP95LatencyAboveFiveHundredMillis`.

Complementar aos sete runbooks que o ticket T09 nomeia explicitamente -- a saúde geral da Consolidation API não se encaixou perfeitamente em nenhum de "broker, DLQ, gap, Redis, watermark, reconciliação, perda de réplica", então ganha seu próprio arquivo em vez de ser forçado em [watermark.md](watermark.md) (que é especificamente sobre o reconciliation worker ficando desatualizado, um modo de falha diferente).

## Diagnóstico

1. Correlacione o pico de erro/latência com um trace no Jaeger (`http://localhost:16686`, serviço `consolidation-api`) usando o `trace_id` dos logs das requests afetadas.
2. Verifique a saturação do connection pool do Postgres e a latência das queries -- a causa mais comum para ambos os sintomas simultaneamente:
   ```sh
   curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=up{job="consolidation-api"}'
   docker compose logs --tail=100 consolidation-api
   ```
3. Verifique a pressão de recursos do container (CPU/memória) se o alvo de implantação expuser isso (veja o `known_gap` em `CriticalInfrastructureComponentDown` -- métricas de recursos ainda não são coletadas via scrape, veja T10).
4. Se o pico na taxa de erro coincidir com um deploy recente, verifique se ele se correlaciona com um endpoint específico ou se afeta todo o tráfego uniformemente.

## Impacto

Merchants veem falhas ou lentidão em queries de saldo; latência sustentada acima de ~1s corre o risco de acionar o próprio timeout do KrakenD (veja [ADR-011](../adrs/ADR-011-krakend-gateway.md)) e transformar uma resposta lenta em uma falha grave.

## Ação segura

Faça rollback de um deploy recente se o momento (timing) tiver correlação; escale as réplicas da `consolidation-api` se a causa for carga e não um bug (a carga por instância também é visível via `sum by (service) (rate(cashflow_http_requests_total[5m]))` no dashboard); corrija o problema subjacente de query/conexão diretamente se essa for a causa.

## Reprocessamento

Não se aplica -- este alerta é sobre a saúde do caminho de leitura (read-path), não sobre a correção dos dados (data correctness). Nenhuma ação de reconciliação ou de DLQ se segue a ele por si só.

## Validação numérica

```sh
curl -s http://localhost:9090/api/v1/query --data-urlencode \
  'query=sum(rate(cashflow_http_failures_total{service="consolidation-api"}[5m]))/sum(rate(cashflow_http_requests_total{service="consolidation-api"}[5m]))'
curl -s http://localhost:9090/api/v1/query --data-urlencode \
  'query=histogram_quantile(0.95, sum(rate(cashflow_http_request_duration_seconds_bucket{service="consolidation-api"}[5m])) by (le))'
```

## Condição de encerramento

Taxa de erro <= 5% e p95 <= 500ms, ambos mantidos por 5 minutos consecutivos.
