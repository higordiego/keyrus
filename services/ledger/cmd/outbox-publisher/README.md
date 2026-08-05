# Outbox Publisher

O publisher é um runtime separado. Ele lê apenas `ledger.outbox_event`, utiliza
leases do PostgreSQL com expiração, publica mensagens AMQP persistentes com RabbitMQ
publisher confirms, e marca um evento apenas após o confirm.

Configuração obrigatória:

- `OUTBOX_POSTGRES_DSN`: credenciais para a role dedicada de banco de dados do publisher;
- `OUTBOX_RABBITMQ_URL`: credenciais dedicadas do publisher. Produção requer
  `amqps://`; o desenvolvimento local deve optar pelo uso (opt in) com
  `OUTBOX_RABBITMQ_ALLOW_INSECURE=true`.

Entradas TLS opcionais são `OUTBOX_RABBITMQ_CA_FILE`,
`OUTBOX_RABBITMQ_CERT_FILE`, e `OUTBOX_RABBITMQ_KEY_FILE`. Health e metrics
são expostos no `OUTBOX_HTTP_ADDRESS` (padrão `:8080`) em `/livez`, `/readyz`,
e `/metrics`. A readiness pertence a este publisher e inclui o PostgreSQL e
o RabbitMQ; ela nunca é consultada pela readiness da API do Ledger.

Cada worker possui uma conexão com o RabbitMQ. A readiness do publisher é deliberadamente
fail-closed: O PostgreSQL deve ter cada privilégio de `SELECT`/`UPDATE` utilizado pelos
caminhos de claim, retry, e confirm, e cada broker configurado do worker deve estar
ready. Capacidade parcial do broker portanto retorna `503`. O lease deve exceder
o confirm timeout por pelo menos dois segundos; porque workers realizam claim de um evento de cada
vez, aquele orçamento (budget) cobre o único item no lease in flight. O shutdown cancela a
E/S (I/O) do AMQP e aguarda até dez segundos para cada worker realizar o join.
