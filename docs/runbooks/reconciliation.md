# Runbook: Executando e interpretando a reconciliação

Este é o runbook de uso geral para o próprio reconciliation-worker (T08): como executá-lo e como interpretar o seu resultado. Para o alerta específico "the worker has stopped proving anything" (o worker parou de provar qualquer coisa), veja [watermark.md](watermark.md); para "it proved a DLQ item exists" (provou que um item na DLQ existe), veja [dlq.md](dlq.md); para um position gap, veja [gap.md](gap.md).

## Executando uma reconciliação sob demanda (on demand)

```sh
docker compose exec reconciliation-worker \
  reconciliation-worker run <merchant_id> <business_date>
```

Isso busca o watermark atual do merchant no Ledger (`GetMerchantWatermark`), e então compara o stream do Ledger até aquele corte (cut) com a projeção Consolidated (`services/consolidation/internal/reconciliation/worker.go`). É seguro executar repetidamente: repetir exatamente o mesmo corte é um no-op (provado por `TestReconcile_RepeatSameCut_NoEffect`), e nunca pode sobrescrever um resultado mais novo já persistido com um mais antigo (`TestReconcile_NewerVersionNeverOverwritten`), mesmo sob execução concorrente (`TestReconcile_ConcurrentCut`).

## Interpretando o resultado

```sql
SELECT source_position_cut, missing_entries, extra_entries, duplicated_entries,
       financial_difference_minor, started_at, completed_at, duration_ms
FROM consolidation.reconciliation_run
WHERE merchant_id = $1 AND business_date = $2;
```

- **`missing_entries` > 0**: presente no Ledger, ausente na projeção Consolidated -- quase sempre um gap ativo ou resolvido-mas-não-reconciliado; veja [gap.md](gap.md).
- **`extra_entries` > 0**: presente na projeção Consolidated, ausente do Ledger naquele corte (cut). Investigue antes de assumir que isso é benigno -- pode significar que uma entrega (delivery) chegou após o corte ter sido feito (execute novamente com um corte mais recente) ou, mais seriamente, um problema de integridade de dados na projeção.
- **`duplicated_entries` > 0**: o mesmo `entry_id` aparece mais de uma vez em `consolidation.inbox_event` para aquele merchant/date -- um invariante do projector que deveria ser impossível em operação normal (`UNIQUE (merchant_id, position)` mais as checagens de conflito em `services/consolidation/internal/adapters/outbound/postgres/store.go`); trate qualquer valor diferente de zero como um bug report, não ruído de rotina.
- **`financial_difference_minor` > 0**: a diferença absoluta entre o net (credits − debits) do Ledger e da projeção no corte, em minor currency units. Isso pode ser diferente de zero mesmo quando as três contagens de entries acima são todas zero, se os valores (amounts) foram registrados incorretamente para uma entry correspondente -- sempre verifique esse número mesmo quando as contagens parecerem limpas.

Nenhum desses campos jamais carrega a description ou o amount da entry por si só (apenas diffs agregados), então esta tabela é segura para consultar e fazer log sem redação (redaction) (veja [ADR-010](../adrs/ADR-010-log-redaction.md)).

## Reprocessando a DLQ

Veja [dlq.md](dlq.md) para o comando protegido `reconciliation-worker dlq` e a sua trilha de auditoria (audit trail) (`consolidation.dlq_reprocess_audit`).

## Validação numérica

A condição de sucesso para qualquer reprocessamento é **exatamente um** efeito financeiro por item -- não "no máximo um". Após o reprocessamento, execute novamente a reconciliação em um corte que inclua o item reprocessado e confirme que todos os quatro campos de divergência leiam zero.

## Condição de fechamento

`missing_entries = extra_entries = duplicated_entries = 0` e `financial_difference_minor = 0` para a execução no corte relevante.
