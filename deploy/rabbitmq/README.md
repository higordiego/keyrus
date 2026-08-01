# RabbitMQ topology owned by the Ledger publisher

The publisher declares this topology idempotently on its dedicated virtual host:

| Resource | Name | Properties |
| --- | --- | --- |
| Topic exchange | `ledger.events` | durable |
| Consolidation queue | `consolidation.ledger-entry-confirmed.v1` | durable quorum queue |
| Binding key | `ledger.entry.confirmed.v1` | exchange to queue |
| Dead-letter exchange | `ledger.events.dlx` | durable topic exchange |
| Dead-letter queue | `consolidation.ledger-entry-confirmed.v1.dlq` | durable quorum queue |

The publishing identity needs configure/write access to these names and no
consume permission in production. Consumer and operational identities are
separate. Production transport is TLS (`amqps://`) and may additionally use a
client certificate. Local plaintext AMQP is accepted only when the publisher's
explicit insecure-development switch is enabled.

Messages use delivery mode `persistent`, routing key and type
`ledger.entry.confirmed.v1`, and carry stable `event_id`, `entry_id`, merchant
position, and W3C `traceparent` headers. Free-form description is never present.
