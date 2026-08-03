# Fluxo de Caixa para Comerciantes

Sistema de controle de lançamentos financeiros (débitos/créditos) e consolidado diário. O design central é simples: uma falha ou queda do serviço de Consolidado nunca pode impedir o comerciante de registrar um lançamento, nem mesmo sob o pico de 50 RPS definido pro serviço de consulta.

Este README é a âncora do projeto. Cada seção aponta pra documentação de referência correspondente, sem duplicar conteúdo.

## Sumário

- [Visão geral e arquitetura](#visão-geral-e-arquitetura)
- [Como rodar localmente](#como-rodar-localmente)
- [Estrutura do repositório](#estrutura-do-repositório)
- [Contratos](#contratos-protobuf-openapi-e-eventos)
- [Testes e evidências](#testes-e-evidências)
- [Segurança e DevSecOps](#segurança-e-devsecops)
- [Implantação](#implantação)
- [Fronteiras de protocolo](#fronteiras-de-protocolo)

## Visão geral e arquitetura

O sistema registra débitos e créditos por comerciante com idempotência e estorno imutável (nunca edição ou exclusão), e mantém um consolidado diário (créditos, débitos, líquido e saldo acumulado) atualizado de forma assíncrona e eventualmente consistente, sempre deixando claro se aquele valor já é definitivo.

A documentação de arquitetura, legado e transição começa pelo [índice](docs/migracao-de-legado.md), que aponta pra quatro arquivos em ordem: [O Legado e os Incidentes Reais](docs/legado-e-incidentes.md), [Arquitetura Alvo](docs/arquitetura-alvo.md) (diagrama completo e o caminho de uma requisição), [Arquitetura de Transição](docs/arquitetura-de-transicao.md) (migração fase a fase) e [Defesa do Modelo](docs/defesa-arquitetural.md) (tabela problema/mecanismo e a jornada de implementação).

A garantia central do desenho: o Ledger nunca chama nem espera o Consolidado. A confirmação de um lançamento é uma transação única no schema `ledger` (entry, idempotência, posição e outbox); a propagação pro Consolidado acontece depois, de forma assíncrona via RabbitMQ, e pode atrasar sem afetar a escrita.

## Como rodar localmente

Pré-requisito: Go 1.26.5. `buf`, `protoc-gen-go`, `protoc-gen-go-grpc` e os plugins do grpc-gateway são baixados em versões fixas pra `.tools/bin`; não precisa instalar `buf` ou `protoc` globalmente.

```sh
make bootstrap        # instala ferramentas pinadas e gera contratos
make ci                # geração determinística, lint, breaking-check, build, testes -race, catálogo BDD
make full-validation   # ci + policy + security (Gitleaks/Govulncheck/Trivy) + build reprodutível/SBOM
make reports           # consolida evidências em evidence/reports/
```

Os testes de integração/E2E (`make ci`, pacotes `test/integration`, `services/*/internal/**/*_integration_test.go`) sobem PostgreSQL, RabbitMQ, Keycloak e KrakenD reais via Testcontainers a cada execução, sem depender de nenhum serviço externo já provisionado. Uma stack local single-command (Docker Compose) é entregável do ticket de Compose/Swarm e ainda está em andamento; até lá, a forma de ver o sistema rodando de ponta a ponta é pela suíte de testes real acima.

## Estrutura do repositório

- [`cmd/README.md`](cmd/README.md) e [`internal/README.md`](internal/README.md): binários e código compartilhado na raiz.
- `services/ledger` e `services/consolidation`: domínio, aplicação e adapters de cada serviço, com fronteira de `internal` própria (um serviço não importa implementação do outro). Ver [`services/ledger/migrations/README.md`](services/ledger/migrations/README.md) e [`services/ledger/cmd/outbox-publisher/README.md`](services/ledger/cmd/outbox-publisher/README.md).
- `proto/cashflow/{ledger,consolidation}`: contratos públicos e internos (Protobuf).
- `api/{openapi,descriptors,events}` e `gen/go`: artefatos gerados e versionados a partir dos contratos.
- `features` e `features/implemented_scenarios.txt`: 14 funcionalidades, 81 tags `@SCN-*`, e a allowlist única de cenários com binding real (ver [Testes e evidências](#testes-e-evidências)).
- [`test/README.md`](test/README.md): harness Godog/PostgreSQL/RabbitMQ e organização dos testes de integração/E2E.
- `deploy/`: ver [Implantação](#implantação).

## Contratos (Protobuf, OpenAPI e eventos)

[`docs/contracts.md`](docs/contracts.md) descreve como o `.proto` gera stubs Go, adapters grpc-gateway e OpenAPI público, e como o baseline de breaking-changes (`api/descriptors/baseline.binpb`) é mantido.

## Testes e evidências

- [`docs/testing-strategy.md`](docs/testing-strategy.md): níveis de teste (catálogo, unitário, contrato, integração de componente, BDD executável, E2E/fitness, desempenho) e o que cada status (aprovado, pendente, implementado, verificado) significa na prática, incluindo como o Godog seleciona cenários pelo manifesto `implemented_scenarios.txt`.
- [`docs/testing-traceability.md`](docs/testing-traceability.md): matriz ligando cada RF/RNF ao cenário `.feature`, nível de teste e evidência esperada, pros 81 cenários aprovados.

## Segurança e DevSecOps

- [`docs/devsecops.md`](docs/devsecops.md): pipeline de CI/CD, gates de policy/security/build-validation, e o que cada workflow do GitHub Actions tem (e não tem) permissão de fazer.
- [`docs/security-exceptions.md`](docs/security-exceptions.md): política de exceção de segurança (segredo nunca é passível de exceção).
- [`SECURITY.md`](SECURITY.md): como reportar uma vulnerabilidade.

## Implantação

- [`deploy/README.md`](deploy/README.md): visão geral dos artefatos de containers e orquestração.
- [`deploy/edge/README.md`](deploy/edge/README.md): KrakenD Community como única borda pública L7, incluindo rotas permitidas, JWT na borda e propagação de `Idempotency-Key`/trace context.
- [`deploy/identity/keycloak/README.md`](deploy/identity/keycloak/README.md): realm, clients e escopos do Keycloak (Authorization Code + PKCE).
- [`deploy/rabbitmq/README.md`](deploy/rabbitmq/README.md): topologia declarativa (exchange, quorum queue, DLX/DLQ) de propriedade do publisher da Ledger.

A arquitetura alvo de produção (Docker Swarm, réplicas, PostgreSQL/RabbitMQ gerenciados) está descrita em [`docs/arquitetura-alvo.md`](docs/arquitetura-alvo.md).

## Fronteiras de protocolo

- HTTP/JSON existe somente na borda pública, gerado via grpc-gateway a partir dos contratos Protobuf.
- Chamadas síncronas entre serviços usam gRPC/Protobuf. `LedgerInternalService` não recebe annotations HTTP e não aparece no OpenAPI público.
- Atualizações assíncronas usam o evento versionado `ledger.entry.confirmed.v1` sobre AMQP/RabbitMQ. Nenhuma chamada ao Consolidado participa da confirmação da Ledger.
