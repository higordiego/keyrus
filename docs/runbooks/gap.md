# Runbook: Gap persistente na posição do merchant

Acionado por: `MerchantPositionGapPersisting` (`consolidation_consumer_gaps > 0`, sustentado por 30s).

## Diagnóstico

1. Identifique o(s) merchant(s) afetado(s) e a posição ausente:
   ```sql
   SELECT merchant_id, source_position, applied_position, first_gap, gap_detected_at
   FROM consolidation.merchant_progress
   WHERE source_position > applied_position;
   ```
   `first_gap` é a primeira posição ausente que está impedindo `applied_position` de avançar (veja `services/consolidation/internal/adapters/outbound/postgres/store.go`).
2. Encontre onde essa posição específica está atualmente:
   ```sql
   SELECT event_id, failure_class, error_code, attempts, last_failed_at
   FROM consolidation.event_pending
   WHERE merchant_id = $1;
   ```
   - `failure_class = 'retry'`: ainda realizando retentativas, se auto-recuperará assim que a causa transitória for resolvida.
   - `failure_class = 'dlq'`: isolado permanentemente até ser reprocessado -- vá para [dlq.md](dlq.md).
   - Nenhuma linha encontrada: o evento pode ainda estar em voo (in flight) pelo RabbitMQ, ou nunca foi publicado (verifique [broker.md](broker.md) e a outbox do Ledger para esse merchant/posição).

## Impacto

O saldo do merchant está correto até `applied_position - 1` e não incluirá nada na posição do gap ou posterior até que ele seja fechado -- entregas fora de ordem em posições superiores ficam retidas, não descartadas (veja os cenários de ordenação em `manter-lancamentos-durante-falha.feature`).

## Ação segura

- Se o gap estiver em `retry`, geralmente nenhuma ação é necessária; confirme se as `attempts`/`last_failed_at` ainda estão avançando e se o backoff de retentativa não estagnou.
- Se estiver na `dlq`, siga [dlq.md](dlq.md) -- não tente "pular" a posição fabricando uma entrada sintética.
- Se o evento nunca foi publicado, verifique a própria outbox do Ledger para aquele merchant/posição (`ledger.outbox_event`) e o [broker.md](broker.md).

## Reprocessamento

O reprocessamento (através do caminho da DLQ ou assim que o retry for bem-sucedido) aplica a posição ausente; `consolidation_consumer_gaps` zera automaticamente assim que `applied_position` alcança `source_position` -- não há um passo separado de "fechar o gap" além de resolver o que quer que esteja bloqueando aquela única posição.

## Validação numérica

```sh
curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=consolidation_consumer_gaps'
```
```sql
SELECT merchant_id, source_position, applied_position FROM consolidation.merchant_progress
WHERE merchant_id = $1;
-- applied_position deve igualar source_position uma vez resolvido
```

## Condição de encerramento

`consolidation_consumer_gaps == 0`.
