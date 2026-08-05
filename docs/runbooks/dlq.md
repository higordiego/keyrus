# Runbook: Dead Letter Queue não está vazia

Acionado por: `DeadLetterQueueNotEmpty` (`consolidation_consumer_pending_dlq > 0`).

## Diagnóstico

1. Descubra o que está na DLQ e por quê:
   ```sql
   SELECT event_id, merchant_id, business_date, error_code, recorded_at
   FROM consolidation.dead_letter_event
   ORDER BY recorded_at DESC;
   ```
   O `error_code` vem de `application.ClassifyFailure` (services/consolidation/internal/application/projector.go): qualquer coisa que cair aqui falhou na validação ou atingiu um conflito persistido, não um erro transitório -- um erro transitório teria permanecido em `retry` (`event_pending.failure_class = 'retry'`), que nunca chega a esta tabela.
2. Correlacione com o `trace_id` nos logs do consumer para ver o payload original e o erro de validação exato (nunca faça log do próprio payload -- veja [ADR-010](../adrs/ADR-010-log-redaction.md)).
3. Causas raízes comuns: um payload que falha em `domain.EntryConfirmed.Validate()` (UUID malformado, `event_type` não suportado, moeda não-BRL, `original_entry_id` faltando), ou um genuíno identity conflict (`ConflictError`: `event_id`/`entry_id`/posição reutilizados com conteúdo diferente).

## Impacto

O saldo do merchant afetado para a `business_date` do item para de avançar até que o item seja resolvido -- indefinidamente, não apenas até que um limite de retentativas (retry budget) expire. A métrica `consolidation_consumer_gaps` também pode ser diferente de zero para o mesmo merchant se as posições posteriores estiverem esperando por esta (veja [gap.md](gap.md)).

## Ação segura

1. **Corrija a causa raiz primeiro.** Se o payload estiver malformado devido a um bug no producer, corrija e faça o deploy do producer antes de reprocessar -- reprocessar um item que vai falhar novamente apenas o envia de volta para a DLQ.
2. **Nunca** edite manualmente `dead_letter_event.payload` ou faça `INSERT`/`UPDATE` diretamente nas tabelas `consolidation.*` para "consertar" o saldo. Isso ignora todas as invariantes que o projector impõe (veja `services/consolidation/internal/application/projector.go`) e não pode ser distinguido posteriormente de um evento financeiro real.
3. Reprocesse através do comando protegido, nunca com uma query manual:
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
   Isso exige um bearer token cuja identidade contenha `ops:reconcile` (`auth.ScopeOpsReconcile`), verificado contra o mesmo emissor OIDC/JWKS em que todas as outras superfícies confiam (veja `runDLQCommand` em `services/consolidation/cmd/reconciliation-worker/main.go` e [ADR-008](../adrs/ADR-008-reconciliation-worker.md)). Ele é auditado: toda execução insere exatamente uma linha em `consolidation.dlq_reprocess_audit` registrando o subject do token verificado como `actor`, `requested_at`/`completed_at`, e `reprocessed_count`/`failed_count` -- independentemente do resultado, incluindo uma execução que reprocesse zero itens.
   **Gap conhecido:** o realm atualmente provisiona apenas o cliente do *serviço* `cashflow-reconciliation-svc` para este escopo (veja `deploy/identity/keycloak/realm-cashflow.json`) -- ainda não há uma identidade de operador humano separada, então o `actor` de auditoria hoje é o subject dessa conta de serviço, não um operador individual. Provisionar um cliente por operador (ou federar para uma identidade SSO) com o mesmo escopo `ops:reconcile` é rastreado separadamente, e não foi feito como parte da T08/T09.
4. Verifique a trilha de auditoria (audit trail):
   ```sql
   SELECT actor, requested_at, completed_at, reprocessed_count, failed_count, outcome
   FROM consolidation.dlq_reprocess_audit
   ORDER BY requested_at DESC LIMIT 5;
   ```
   `outcome = 'partial_failure'` significa que pelo menos um item ainda está falhando e permanece na DLQ para a próxima tentativa -- ele nunca é descartado silenciosamente.

## Validação numérica

Cada item reprocessado deve produzir **exatamente um** efeito financeiro -- não zero (descartado silenciosamente) e não mais de um (aplicado duas vezes):
```sql
-- O saldo antes/depois para o merchant/data afetados deve diferir em exatamente
-- o valor do item reprocessado, uma vez.
SELECT credits_minor, debits_minor, entry_count FROM consolidation.daily_balance
WHERE merchant_id = $1 AND business_date = $2;
```
Em seguida, execute o reconciliation worker para o merchant/data afetados no momento (cut) pós-reprocessamento e confirme que a divergência é zero:
```sh
docker compose exec reconciliation-worker reconciliation-worker run <merchant_id> <business_date>
curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=reconciliation_last_run_missing_entries'
curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=reconciliation_last_run_extra_entries'
curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=reconciliation_last_run_duplicated_entries'
```

## Condição de encerramento

`consolidation_consumer_pending_dlq == 0` **e** a execução da reconciliação para o merchant/data afetados no momento (cut) pós-reprocessamento reporta zero entradas ausentes, extras e duplicadas -- verificar a profundidade da DLQ isoladamente não é suficiente, porque um item reprocessado-mas-que-ainda-diverge limparia a DLQ sem realmente estar correto.
