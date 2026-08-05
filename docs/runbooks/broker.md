# Runbook: RabbitMQ / broker degradado ou inacessível

Acionado por: `OutboxOldestPendingTooOld`, `RecoveryPipelineErrorsIncreasing`, `CriticalInfrastructureComponentDown` (component=infrastructure, job=rabbitmq ou keycloak).

## Diagnóstico

1. Verifique se o broker está realmente ativo:
   ```sh
   docker compose ps rabbitmq
   curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=up{job="rabbitmq"}'
   ```
2. Se `up == 0`, verifique os logs do container e a management UI:
   ```sh
   docker compose logs --tail=100 rabbitmq
   open http://localhost:15672   # cashflow / $RABBITMQ_PASSWORD
   ```
3. Se `up == 1` mas o `OutboxOldestPendingTooOld` está disparando, o broker em si está saudável, mas o *publisher* não está drenando a `ledger.outbox_event`. Verifique:
   ```sh
   docker compose logs --tail=100 ledger-outbox-publisher
   curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=outbox_publish_errors_total'
   ```
   Um `outbox_publish_errors_total` aumentando com um `outbox_pending` estável significa que as tentativas de publicação estão falhando (credenciais incorretas, incompatibilidade de topologia, falha no handshake TLS) e não que a fila está sem consumers.
4. Para `CriticalInfrastructureComponentDown{job="keycloak"}`: o Keycloak fora do ar bloqueia toda emissão de novo token e atualização de JWKS; os tokens existentes continuam funcionando até expirarem (short-lived, veja o ADR-004). Verifique `docker compose logs --tail=100 keycloak` para falhas de conexão no DB ou na importação do realm.

## Impacto

- O Ledger continua aceitando e persistindo duravelmente as entradas (o padrão outbox, ADR-006, existe justamente para que uma interrupção do broker nunca bloqueie as escritas) -- mas a projeção Consolidated para de receber novos eventos até que o publisher se recupere.
- Uma indisponibilidade do Keycloak não revoga os tokens já emitidos, mas bloqueia novos logins/atualizações de token e qualquer fluxo de client-credentials do qual o comando protegido `dlq` do reconciliation-worker dependa (veja [reconciliation.md](reconciliation.md)).

## Ação segura

- **RabbitMQ container inativo**: reinicie-o (`docker compose restart rabbitmq`). Filas de quórum (veja [`deploy/rabbitmq/README.md`](../../deploy/rabbitmq/README.md)) sobrevivem a uma reinicialização com seus dados intactos, desde que o volume `rabbitmq-data` não seja removido.
- **Publisher falhando ao conectar/publicar**: verifique se `OUTBOX_RABBITMQ_URL`/`OUTBOX_RABBITMQ_ALLOW_INSECURE` (ou o material TLS em produção) correspondem às credenciais reais do broker; uma rotação de credencial em um lado sem o outro é a causa mais comum.
- **Nunca** expurgue manualmente as linhas de `ledger.outbox_event` ou as filas do RabbitMQ para "limpar" o alerta -- isso descarta eventos financeiros persistidos duravelmente. Corrija o problema de conectividade/credencial e deixe o publisher drenar o backlog.

## Reprocessamento

O publisher da outbox realiza retentativas automaticamente (`outbox_publish_attempts_total` é incrementado por evento confirmado) e não exige nenhum reprocessamento manual assim que a conectividade for restaurada. Se um evento específico parecer estar preso (stuck) por vários ciclos de retry, correlacione o seu `event_id` de `ledger.outbox_event` com os logs do publisher antes de escalar -- não delete a linha.

## Validação numérica

```sh
curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=outbox_pending'
curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=outbox_oldest_age_seconds'
curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=up{job=~"rabbitmq|keycloak"}'
```

## Condição de encerramento

`outbox_oldest_age_seconds <= 30` por 2 minutos consecutivos, `up{job=~"rabbitmq|keycloak"} == 1` por 2 minutos consecutivos, e nenhum aumento adicional em `outbox_publish_errors_total`/`consolidation_consumer_retry_total`/`reconciliation_errors_total` por 10 minutos -- combinando exatamente com as anotações `closing_condition` dos próprios alertas.
