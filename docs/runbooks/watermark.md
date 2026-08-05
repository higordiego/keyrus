# Runbook: Ledger watermark não verificável

Acionado por: `LedgerWatermarkNotVerifiable` (`reconciliation_seconds_since_last_run < 0` ou `> 900`).

Isso não é "o Ledger e o Consolidado discordam" -- isso seria o disparo do `DeadLetterQueueNotEmpty` / `MerchantPositionGapPersisting` em vez disso, com uma divergência real medida. Esse alerta significa que o próprio reconciliation-worker parou de produzir *prova* (proof) de um jeito ou de outro: nenhuma evidência de convergência, e nenhuma de divergência também.

## Diagnóstico

1. Confirme se o processo do worker está realmente executando e não em crash-looping:
   ```sh
   docker compose ps reconciliation-worker
   docker compose logs --tail=100 reconciliation-worker
   curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=up{job="reconciliation-worker"}'
   ```
2. Se o processo estiver de pé, mas `reconciliation_seconds_since_last_run` continua subindo, verifique em que a sua última tentativa de execução realmente falhou:
   ```sh
   curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=reconciliation_errors_total'
   ```
   Um `reconciliation_errors_total` subindo junto com um `seconds_since_last_run` subindo significa que toda tentativa está falhando -- veja `streamSourceEntries` em `services/consolidation/internal/reconciliation/worker.go`: após `maxStreamAttempts` (3) reaberturas de stream falhas, o `Reconcile` retorna um erro e não persiste nada.
3. Causas mais prováveis, em ordem de frequência:
   - A conectividade gRPC para o Ledger está quebrada (expiração do certificado mTLS, `CASHFLOW_LEDGER_GRPC_TARGET` inacessível, o próprio Ledger fora do ar).
   - As client credentials do `cashflow-reconciliation-svc` são inválidas ou o client secret file (`CASHFLOW_SERVICE_CLIENT_SECRET_FILE`) está obsoleto/rotacionado (stale/rotated) apenas de um lado.
   - A concessão do escopo (scope grant) `ops:reconcile`/`ledger:internal:read` do Ledger para aquele cliente foi removida do realm (`deploy/identity/keycloak/realm-cashflow.json`).

## Impacto

Não há prova recente de que o Ledger (source) e a projeção Consolidated concordam. Isso **não** significa que eles divergiram -- apenas que ninguém verificou recentemente. Trate isso como "desconhecido" (unknown), não "conhecido como ruim" (known-bad), mas escale com a mesma urgência: uma divergência não detectada durante esta janela também passaria despercebida.

## Ação segura

- Restaure a conectividade do gRPC/credenciais conforme o diagnóstico acima; o loop do daemon do worker (um ticker de 10 segundos, veja `runDaemon` em `services/consolidation/cmd/reconciliation-worker/main.go`) é retomado automaticamente assim que conseguir alcançar o Ledger novamente -- não é necessário reiniciar manualmente a própria lógica de reconciliação, apenas a dependência que estava quebrada.
- Se o container do worker em si estiver fora do ar, reinicie-o: `docker compose restart reconciliation-worker`.

## Reprocessamento

Não se aplica diretamente -- este alerta é sobre o *prover* (provador) estar indisponível, não sobre uma divergência comprovada. Uma vez que a conectividade seja restaurada e uma execução seja concluída, verifique o seu resultado real (veja [reconciliation.md](reconciliation.md)); se ele reportar entries missing/extra/duplicated diferentes de zero, siga [dlq.md](dlq.md) ou [gap.md](gap.md) conforme apropriado para o que ele encontrou.

## Validação numérica

```sh
curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=reconciliation_seconds_since_last_run'
curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=reconciliation_runs_total'
curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=reconciliation_errors_total'
```

## Condição de fechamento

`reconciliation_seconds_since_last_run` cai de volta para menos de 900 **e** `reconciliation_errors_total` para de aumentar por 15 minutos -- uma única execução bem-sucedida por sorte logo após uma longa indisponibilidade não é o suficiente por si só para declarar isso como fechado.
