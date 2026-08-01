# Outbox Publisher

The publisher is a separate runtime. It reads only `ledger.outbox_event`, uses
expiring PostgreSQL leases, publishes persistent AMQP messages with RabbitMQ
publisher confirms, and marks an event only after the confirm.

Required configuration:

- `OUTBOX_POSTGRES_DSN`: credentials for the dedicated publisher database role;
- `OUTBOX_RABBITMQ_URL`: dedicated publisher credentials. Production requires
  `amqps://`; local development must opt in with
  `OUTBOX_RABBITMQ_ALLOW_INSECURE=true`.

Optional TLS inputs are `OUTBOX_RABBITMQ_CA_FILE`,
`OUTBOX_RABBITMQ_CERT_FILE`, and `OUTBOX_RABBITMQ_KEY_FILE`. Health and metrics
are exposed on `OUTBOX_HTTP_ADDRESS` (default `:8080`) at `/livez`, `/readyz`,
and `/metrics`. Readiness belongs to this publisher and includes PostgreSQL and
RabbitMQ; it is never consulted by Ledger API readiness.
