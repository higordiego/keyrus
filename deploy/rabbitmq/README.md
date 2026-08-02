# RabbitMQ topology owned by the Ledger publisher

The publisher declares this topology idempotently on its dedicated virtual host:

| Resource | Name | Properties |
| --- | --- | --- |
| Topic exchange | `ledger.events` | durable |
| Legacy consolidation queue | `consolidation.ledger-entry-confirmed.v1` | exact `0ac21b5` quorum queue; retained for reversible rollout |
| Active consolidation queue | `consolidation.ledger-entry-confirmed.v2` | durable quorum queue; dead-letter `at-least-once`; overflow `reject-publish` |
| Binding key | `ledger.entry.confirmed.v1` | exchange to queue |
| Dead-letter exchange | `ledger.events.dlx` | durable topic exchange |
| Dead-letter queue | `consolidation.ledger-entry-confirmed.v2.dlq` | durable quorum queue |
| Cutover marker | `ledger.outbox.topology.v2.ready` | durable internal exchange; publisher preflight fails closed when absent |

The publishing identity needs configure/write access to these names and no
consume permission in production. Consumer and operational identities are
separate. Production transport is TLS (`amqps://`) and may additionally use a
client certificate. Local plaintext AMQP is accepted only when the publisher's
explicit insecure-development switch is enabled.

## Upgrade and rollback runbook

The queue arguments added by the hardened release are immutable. Never deploy
the new binary by redeclaring `.v1`; use this versioned cutover:

1. Pause all old and new publishers and verify their worker count is zero.
2. Give a temporary operational identity consume/configure/write permissions.
3. Run `/outbox-topology upgrade`. It first passively verifies the exact
   exchanges and queue arguments shipped by `0ac21b5`, before creating `.v2`.
4. Each `.v1` backlog message is copied directly to the unbound `.v2` queue,
   preserving AMQP properties, headers, body and `event_id`. `.v1` is acked only
   after a publisher confirm from `.v2`. An interrupted command is rerunnable;
   it can duplicate the in-flight identity but cannot lose it.
5. The command binds `.v2`, unbinds `.v1`, and only then creates the durable
   cutover marker. Start the new publishers and verify readiness, pending count,
   oldest age, confirm latency and errors before removing operational access.

For rollback, pause publishers and run `/outbox-topology rollback`. The command
removes the marker first (new binaries immediately fail closed), moves the `.v2`
backlog to the exact legacy queue with the same confirm-before-ack rule, then
binds `.v1` and unbinds `.v2`. Resume only the old publisher. Never delete either
queue until its message identities have been reconciled by operations.

Both commands require `OUTBOX_RABBITMQ_URL`; plaintext also requires
`OUTBOX_RABBITMQ_ALLOW_INSECURE=true`. A preflight mismatch exits with a named
resource and leaves the destination topology untouched. Production CA and
mutual-TLS files use the same `OUTBOX_RABBITMQ_CA_FILE`,
`OUTBOX_RABBITMQ_CERT_FILE` and `OUTBOX_RABBITMQ_KEY_FILE` settings as the
publisher. SIGINT/SIGTERM cancels the current confirm-before-ack operation; the
command can then be rerun without a total-backlog timeout.

Messages use delivery mode `persistent`, routing key and type
`ledger.entry.confirmed.v1`, and carry stable `event_id`, `entry_id`, merchant
position, and W3C `traceparent` headers. Free-form description is never present.
The publisher rejects nested fields named `description`, credentials, secrets,
tokens, authorization data, card numbers, or CVV before sending, and verifies
that body identity and timestamps match the claimed outbox row and AMQP
properties.
