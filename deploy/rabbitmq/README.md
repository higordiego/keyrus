# RabbitMQ topology owned by the Ledger publisher

O publisher declara essa topologia de forma idempotente em seu virtual host dedicado:

| Resource | Name | Properties |
| --- | --- | --- |
| Topic exchange | `ledger.events` | durable |
| Legacy consolidation queue | `consolidation.ledger-entry-confirmed.v1` | exact `0ac21b5` quorum queue; mantida para rollout reversível |
| Active consolidation queue | `consolidation.ledger-entry-confirmed.v2` | durable quorum queue; dead-letter `at-least-once`; overflow `reject-publish` |
| Binding key | `ledger.entry.confirmed.v1` | exchange to queue |
| Dead-letter exchange | `ledger.events.dlx` | durable topic exchange |
| Dead-letter queue | `consolidation.ledger-entry-confirmed.v2.dlq` | durable quorum queue |
| Cutover marker | `ledger.outbox.topology.v2.ready` | durable internal exchange; preflight do publisher falha fechado (fails closed) quando ausente |

A identidade de publicação precisa de acesso de configure/write a esses nomes e nenhuma
permissão de consume em produção. Identidades de consumidor (consumer) e operacionais (operational) são
separadas. O transporte de produção é TLS (`amqps://`) e pode usar adicionalmente um
certificado de cliente. AMQP local em texto claro (plaintext) é aceito apenas quando a chave explícita
de desenvolvimento inseguro (insecure-development) do publisher está habilitada.

## Upgrade and rollback runbook

Os argumentos de queue adicionados pelo release endurecido (hardened) são imutáveis. Nunca faça deploy
do novo binário redeclarando `.v1`; use este cutover versionado:

1. Pause todos os publishers antigos e novos e verifique se o worker count deles é zero.
2. Dê a uma identidade operacional temporária permissões de consume/configure/write.
3. Execute `/outbox-topology upgrade`. Ele primeiro verifica passivamente as
   exchanges exatas e os argumentos de queue enviados pelo `0ac21b5`, antes de criar `.v2`.
4. Cada mensagem do backlog da `.v1` é copiada diretamente para a queue `.v2` desvinculada (unbound),
   preservando propriedades AMQP, headers, body e `event_id`. `.v1` é acked apenas
   após um publisher confirm da `.v2`. Um comando interrompido é rerunnable (pode ser executado novamente);
   ele pode duplicar a identidade in-flight (em andamento), mas não pode perdê-la.
5. O comando faz o bind de `.v2`, unbinds de `.v1` e só então cria o marcador
   de cutover durável. Inicie os novos publishers e verifique readiness, pending count,
   idade mais antiga (oldest age), latência de confirmação e erros antes de remover o acesso operacional.

Para rollback, pause os publishers e execute `/outbox-topology rollback`. O comando
remove o marcador primeiro (novos binários falham fechados imediatamente), move o backlog da `.v2`
para a legacy queue exata com a mesma regra de confirm-before-ack, depois
faz o bind de `.v1` e unbinds de `.v2`. Retome apenas o publisher antigo. Nunca exclua nenhuma
queue até que suas identidades de mensagem tenham sido reconciliadas pelas operações.

Ambos os comandos exigem `OUTBOX_RABBITMQ_URL`; texto claro (plaintext) também exige
`OUTBOX_RABBITMQ_ALLOW_INSECURE=true`. Um descompasso de preflight (mismatch) sai com um
recurso nomeado e deixa a topologia de destino intocada. Os arquivos de CA de produção e
mutual-TLS usam as mesmas configurações `OUTBOX_RABBITMQ_CA_FILE`,
`OUTBOX_RABBITMQ_CERT_FILE` e `OUTBOX_RABBITMQ_KEY_FILE` que o
publisher. SIGINT/SIGTERM cancela a operação atual de confirm-before-ack; o
comando pode então ser executado novamente sem um timeout do backlog total.

As mensagens usam modo de entrega `persistent`, routing key e tipo
`ledger.entry.confirmed.v1`, e carregam headers estáveis de `event_id`, `entry_id`, posição do merchant
e `traceparent` da W3C. Descrição de formato livre nunca está presente.
O publisher rejeita campos aninhados chamados `description`, credentials, secrets,
tokens, dados de autorização, números de cartão ou CVV antes de enviar, e verifica
se a identidade do body e timestamps correspondem à linha de outbox reivindicada e propriedades
AMQP.
