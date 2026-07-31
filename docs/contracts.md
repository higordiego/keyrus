# Contratos e evolução

Os `.proto` são a fonte dos stubs Go, adapters grpc-gateway e OpenAPI público. O descriptor `api/descriptors/baseline.binpb` é o baseline inicial do gate de breaking changes; `current.binpb` representa o estado atual.

O baseline foi atualizado nesta correção final do T01 somente porque os contratos ainda não foram publicados e o repositório não possui release nem commit anterior. Depois da publicação inicial, mudanças devem ser comparadas contra o baseline versionado; sobrescrevê-lo para ocultar incompatibilidades é proibido.

O evento `ledger.entry.confirmed.v1` usa centavos inteiros e BRL. Campos adicionais são compatíveis e podem ser ignorados por consumidores v1; alteração incompatível exige novo `event_type` e novo schema.

Os dois comandos públicos (`POST /v1/entries` e `POST /v1/entries/{entryId}/reversals`) declaram no OpenAPI gerado o header obrigatório `Idempotency-Key`, sucesso `201` e respostas de validação, autenticação/autorização e conflito `409`; o estorno também documenta o `404` anti-enumeração. O enforcement, o mapeamento HTTP e a idempotência transacional pertencem aos tickets de runtime — esta fundação entrega o contrato, não um mock de sucesso.

Cada resultado diário é um envelope com data, estado, motivo, definitividade e posições da fonte/aplicação. Os valores do snapshot ficam em `data`. Uma pendência conhecida sem snapshot serializa `data: null` e preserva o envelope (`@SCN-RF05-007`); isso não se confunde com snapshot inacessível ou não comprovável, documentado como indisponibilidade `503` (`@SCN-RF05-008`).

`LedgerEntry.state` usa `EntryState`: `CONFIRMED` para lançamentos duravelmente confirmados e `REVERSED` para o original após um estorno. A compensação é um novo lançamento `CONFIRMED` com `original_entry_id`; o original continua imutável e referencia a compensação por `reversal_entry_id`.

A identidade JWT e os requisitos mTLS/JWT/deadline do gRPC continuam como políticas de transporte e segurança a implementar nos tickets posteriores. `LedgerInternalService` permanece exclusivamente gRPC e não é exposto no OpenAPI público.
