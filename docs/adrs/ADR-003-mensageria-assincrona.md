# ADR-003: Comunicação Assíncrona via RabbitMQ

## O Problema
Com a adoção do modelo CQRS (separação da escrita e da leitura descrita na ADR-001) e isolamento dos bancos de dados (ADR-002), o desafio passou a ser a integração: como avisar o Consolidado de forma infalível que a Ledger API gravou uma nova transação? Fazer a Ledger chamar a API do Consolidado via HTTP síncrono violaria a premissa de desacoplamento, pois a queda da leitura afetaria a gravação.

## A Decisão
Adotamos o **RabbitMQ** operando com filas resilientes (`Quorum Queues`) para o transporte assíncrono dos eventos de transação.

Sempre que a Ledger API grava algo, ela (através do Outbox Publisher) enfileira um evento `ledger.entry.confirmed.v1` no RabbitMQ. O Consolidation Consumer lê esse evento no seu próprio tempo, processa a conta e atualiza os saldos agregados, retornando o ACK ao broker apenas quando finaliza o commit do saldo com sucesso.

## Consequências Positivas
* **Absorção de Picos (Buffer):** Durante a Black Friday ou horário de pico, se chegarem 1.000 requisições por segundo, o RabbitMQ armazena os eventos temporariamente sem estressar o banco do Consolidado, que irá trabalhar na velocidade máxima suportada até esvaziar a fila.
* **Desacoplamento Temporal:** A Ledger API desconhece o status ou até a existência do Consolidado. Se a API de leitura ficar fora do ar para manutenção por 1 hora, o sistema continua vendendo e, quando a leitura voltar, ela processa todas as mensagens represadas sem perda de dados.
* **Garantia de Entrega (At-Least-Once):** Mensagens não se perdem caso um serviço falhe abruptamente, elas retornam para a fila ou vão para uma DLQ (Dead Letter Queue).

## Consequências Negativas
* **Maior complexidade de Operação:** Adicionamos um serviço infraestrutural crítico à nossa stack, que requer manutenção, monitoramento e runbooks próprios.
* **Garantia de Idempotência Obrigatória:** Como a fila garante que entregará "pelo menos uma vez", o Consolidado precisa estar blindado contra receber o mesmo evento mais de uma vez.

## Alternativas Consideradas
* **Kafka:** Apesar de escalar exponencialmente, exige infraestrutura complexa (Zookeeper/KRaft) e foi preterido porque nossa volumetria atual (50 RPS a picos de milhares) é confortavelmente atendida por um RabbitMQ maduro sem o custo extra operacional.
* **Polling Direto do Banco:** O Consolidado ficar perguntando para o banco da Ledger se "há algo novo". Prejudica a performance do banco-fonte e adiciona acoplamento forte entre os dois domínios.

## Gatilhos de Revisão
* A migração para Kafka ou Kinesis pode ser justificada caso o negócio mude para um padrão de *Event Sourcing* de longo prazo onde outras diversas áreas queiram "repuxar" o histórico de transações meses no passado de forma repetida.
