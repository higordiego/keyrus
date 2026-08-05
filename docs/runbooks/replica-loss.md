# Runbook: Perda de réplica (KrakenD, service instances, RabbitMQ quorum member)

Cobre a perda de uma instância redundante de um componente escalado horizontalmente -- não uma indisponibilidade total do componente (veja [broker.md](broker.md) para isso) e não perda de dados da aplicação.

## KrakenD gateway

Conforme o [ADR-011](../adrs/ADR-011-krakend-gateway.md), o KrakenD é stateless e projetado para escalar horizontalmente; o próprio ADR nomeia "todas as réplicas do KrakenD fora do ar" como o único ponto único de falha real que esta arquitetura aceita. Perder *uma* réplica dentre várias atrás de um load balancer deve ser invisível para os merchants -- as réplicas restantes continuam servindo.

**Diagnóstico**: verifique a contagem e saúde das réplicas em qualquer camada que execute múltiplas instâncias do KrakenD (esta stack do Compose roda exatamente um serviço/container `krakend`, então hoje "perda de réplica" para o KrakenD neste ambiente significa que a única instância está fora do ar -- não há redundância para recorrer até que um alvo de implantação multi-réplica, ex. Swarm com `replicas: N`, esteja no lugar; veja T10).

**Ação segura**: reinicie a réplica/container falha; se estiver usando um load balancer, confirme que ele cancelou o registro da instância não saudável antes que ela se recupere (evite enviar tráfego para uma instância que ainda está iniciando).

**Condição de encerramento**: contagem de réplicas de volta ao alvo configurado, o próprio health check do `krakend` passando.

## Réplicas das APIs Ledger/Consolidation

Tanto `ledger-api` quanto `consolidation-api` são serviços HTTP/gRPC stateless lendo/escrevendo no Postgres; perder uma instância atrás de um load balancer é a mesma história do KrakenD -- as instâncias restantes continuam servindo, contanto que não estejam todas vinculadas ao mesmo domínio de falha.

**Diagnóstico**:
```sh
curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=up{job=~"ledger-api|consolidation-api"}'
```

**Ação segura**: reinicie a instância falha; não reinicie todas as instâncias simultaneamente (perde toda a capacidade de uma vez). Confirme se os limites do connection pool do Postgres (`max_connections`) não estão sendo atingidos pelas instâncias sobreviventes absorvendo a carga da instância perdida.

**Condição de encerramento**: `up == 1` para toda instância esperada, taxa de `cashflow_http_failures_total` de volta à linha de base.

## Perda de RabbitMQ quorum member

A fila ativa do Consolidation (`consolidation.ledger-entry-confirmed.v2`, veja [`deploy/rabbitmq/README.md`](../../deploy/rabbitmq/README.md)) é uma quorum queue durável. Perder um membro de um quórum de múltiplos nós (um Swarm/multi-broker deployment, não esta stack Compose de nó único) não perde dados, contanto que o quórum dos membros restantes esteja intacto.

**Diagnóstico**: RabbitMQ management UI (`http://localhost:15672`) ou `rabbitmq-diagnostics check_running`/`cluster_status` dentro do container.

**Ação segura**: traga o nó perdido de volta e deixe-o retornar ao cluster; o RabbitMQ ressincroniza a quorum queue automaticamente. Não force a exclusão e recriação da fila para "consertar" um quórum degradado -- isso descarta o que quer que os membros sobreviventes tivessem.

**Condição de encerramento**: todos os membros esperados do cluster reportando `running`, contagem do leader/followers da quorum queue de volta ao fator de replicação configurado.

## O que este runbook não cobre

Perda total do broker/DB/gateway (não apenas uma réplica) -- veja [broker.md](broker.md) e [watermark.md](watermark.md). Divergência no nível de dados após uma réplica retornar -- veja [reconciliation.md](reconciliation.md).
