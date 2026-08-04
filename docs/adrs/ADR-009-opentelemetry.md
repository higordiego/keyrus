# ADR-009: Observabilidade Distribuída com OpenTelemetry

## O Problema
Com 5 a 6 componentes separados (Gateway, Ledger, Publisher, RabbitMQ, Consumer, Consolidation) numa chamada transacional ou assíncrona, descobrir em qual pedaço exato da arquitetura uma transação demorou ou sumiu se torna humanamente impossível se olharmos apenas logs isolados sem contexto.

## A Decisão
Todo o ecossistema precisa estar instrumentado de maneira "Vendor-Agnostic" (Sem aprisionamento de fornecedor) usando **OpenTelemetry (OTel)**.

Em todo ponto de integração, os `Trace-IDs` e `Span-IDs` são propagados nos Headers HTTP, Metadados do gRPC e Headers AMQP do RabbitMQ.
Centralizamos os envios em um `OTel Collector` interno. Dessa forma, podemos exportar para Jaeger, Datadog ou Grafana sem precisar tocar no código da aplicação.

## Consequências Positivas
* Uma requisição de entrada tem seu ID unificado de ponta a ponta. Basta procurar o `Trace-ID` em uma barra de pesquisa para ver a cachoeira (waterfall) de todas as execuções, chamadas de banco e tempo de mensageria.
* Zero refatoração de código caso a empresa decida trocar seu software de APM (Application Performance Monitoring).

## Consequências Negativas
* Configurar o OpenTelemetry no RabbitMQ e Go costuma dar um pouco mais de trabalho, já que bibliotecas e contextos (Context propagation em Go) precisam estar cirurgicamente alinhados.
* Um pequeno *overhead* (acréscimo no tempo de resposta) de microsegundos a cada transação para calcular e despachar os rastros ao Collector.

## Gatilhos de Revisão
* N/A. Essa é uma decisão fundamental de plataformas distribuídas modernas e não deve ser desfeita. Ajustes apenas fariam no *sampling rate* (exemplo: exportar apenas 10% dos traces, ou apenas os que apresentarem erros) se o custo de retenção no Grafana/Datadog estourar orçamentos.
