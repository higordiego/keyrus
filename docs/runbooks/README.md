# Runbooks

Cada arquivo abaixo é linkado diretamente do alerta que o aciona através da anotação `runbook_url` do alerta (veja [`deploy/observability/prometheus/rules/cashflow-alerts.yml`](../../deploy/observability/prometheus/rules/cashflow-alerts.yml)). Todo runbook segue a mesma estrutura que o ticket T09 exige por alerta: diagnóstico, impacto, ação segura, reprocessamento (onde aplicável), validação numérica e uma condição de fechamento objetiva -- não apenas um threshold.

| Runbook | Cobre |
| --- | --- |
| [broker.md](broker.md) | RabbitMQ / Keycloak degradado ou inacessível, outbox backlog, erros crescentes na pipeline |
| [dlq.md](dlq.md) | Dead-lettered events: diagnóstico e o comando protegido de reprocessamento (reprocess command) |
| [gap.md](gap.md) | O `source_position > applied_position` de um merchant persistindo |
| [redis.md](redis.md) | Cache -- ainda não implementado; documenta o comportamento alvo (`@SCN-RNF07-006`) para que o runbook exista antes do incidente, não depois |
| [watermark.md](watermark.md) | O próprio reconciliation worker ficando obsoleto (stale) (nenhuma prova recente de convergência, não uma divergência comprovada) |
| [reconciliation.md](reconciliation.md) | Executar a reconciliação sob demanda e interpretar o seu resultado |
| [replica-loss.md](replica-loss.md) | Perda de uma instância redundante do KrakenD, um serviço de API, ou um membro de quorum do RabbitMQ |
| [api-health.md](api-health.md) | Consolidation API error rate / p95 latency degradada (suplementar; não se encaixou nas sete categorias nomeadas acima) |
