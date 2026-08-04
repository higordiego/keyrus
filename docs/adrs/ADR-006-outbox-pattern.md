# ADR-006: Transactional Outbox Pattern

## O Problema
Se nós gravamos o lançamento no banco do Ledger e depois publicamos um evento no RabbitMQ, temos o "Problema do Consenso Distribuído". 
Se o banco gravar com sucesso e o RabbitMQ recusar a mensagem, o saldo no Consolidado nunca será atualizado. Se nós publicarmos no RabbitMQ primeiro e depois o banco falhar, o Consolidado terá saldo duplicado "fantasma".

## A Decisão
Implementamos o **Outbox Pattern**. 

A Ledger API grava o lançamento financeiro E um "evento de outbox" na tabela `outbox_events` na MESMA transação do Postgres. É tudo ou nada. 
Paralelo a isso, um worker independente (`ledger-outbox-publisher`) usa o comando `SKIP LOCKED` do PostgreSQL para ficar drenando essa tabela e publicando as mensagens com segurança no RabbitMQ. A mensagem só é deletada da tabela quando o RabbitMQ responde com um ACK (Confirmação).

## Consequências Positivas
* Integridade Relacional: A transação financeira está eternamente atrelada à emissão do evento. Consistência garantida sem "Two-Phase Commit".
* A Ledger API nunca precisa esperar o RabbitMQ responder, mantendo a latência baixa para o usuário.

## Consequências Negativas
* O banco de dados do Ledger sofre carga extra. Em vez de 1 linha de inserção, fazemos pelo menos 3 (Lançamento, Idempotência e Outbox).
* Cria-se a necessidade de um serviço a mais rodando no background (`outbox-publisher`) que precisa ser monitorado.

## Alternativas Consideradas
* **Change Data Capture (CDC) via Debezium:** Iria ler direto dos arquivos lógicos do banco e jogar para um Kafka. Descartamos porque traz uma complexidade titânica para gerenciar (Kafka Connect) frente à nossa escala atual de RPS.

## Gatilhos de Revisão
* Se a carga do banco de dados (TPS - Transações por Segundo) exceder os limites físicos das máquinas por causa da tabela de outbox crescendo absurdamente rápido, a adoção de CDC (Debezium) se justificará.
