# Fluxo de caixa para comerciantes

Fundação Go orientada a contratos para Ledger e Consolidado Diário. Este ticket não contém regra financeira, persistência, identidade, mensageria nem containers.

## Bootstrap e gates

Pré-requisito: Go 1.26.4. `buf`, `protoc-gen-go`, `protoc-gen-go-grpc` e os plugins do grpc-gateway são baixados em versões fixas para `.tools/bin`; não é necessária instalação global de `buf` ou `protoc`.

```sh
make bootstrap
make ci
make reports
```

`make ci` valida geração determinística, formatação, Buf lint, breaking changes contra o descriptor baseline, `go vet`, build, `go test -race`, parsing Gherkin, as 81 tags únicas e o manifesto de cenários implementados.

## Fronteiras de protocolo

- HTTP/JSON existe somente na borda pública, gerado via grpc-gateway a partir dos contratos Protobuf.
- Chamadas síncronas entre serviços usam gRPC/Protobuf. `LedgerInternalService` não recebe annotations HTTP e não aparece no OpenAPI público.
- Atualizações assíncronas usam o evento versionado `ledger.entry.confirmed.v1` sobre AMQP/RabbitMQ; nenhuma chamada ao Consolidado participa da confirmação da Ledger.

## Layout

- `proto/cashflow/ledger/public/v1` e `proto/cashflow/consolidation/public/v1`: contratos públicos e mappings HTTP.
- `proto/cashflow/ledger/internal/v1`: watermark e streaming de reconciliação exclusivamente gRPC.
- `api/openapi`, `api/descriptors`, `gen/go`: artefatos gerados e versionados.
- `api/events`: JSON Schema do evento assíncrono.
- `features`: 14 funcionalidades e exatamente 81 tags `@SCN-*`.
- `features/implemented_scenarios.txt`: única lista de cenários realmente ligados a steps.
- `test/bdd`: parser, inventário e runner Godog v0.15.1 integrado ao `go test`.

No T01 o manifesto está deliberadamente vazio, pois steps de negócio estão fora de escopo. Um cenário só entra nele junto com bindings reais; `undefined`, `pending` e `skip` não podem declarar sucesso.
