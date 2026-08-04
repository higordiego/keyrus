# Matriz de Conformidade e Evidências do Desafio

Esta matriz conecta os desafios propostos (CH-01 a CH-11) com a implementação real no repositório. Nela você encontra os links diretos para a documentação, o código fonte e as evidências (testes automatizados).

| ID | Requisito do desafio | Estado | Evidência e Links |
| --- | --- | --- | --- |
| CH-01 | Serviço de controle de lançamentos | Verificado | API Go implementada em [`services/ledger`](../services/ledger). Contrato em [`proto/cashflow/ledger`](../proto/cashflow/ledger). Testes BDD em [`features`](../features). |
| CH-02 | Serviço de consolidado diário | Verificado | API em [`services/consolidation`](../services/consolidation). Consumo via RabbitMQ em [`services/consolidation/cmd/consumer`](../services/consolidation/cmd/consumer). |
| CH-03 | Domínios funcionais e capacidades | Verificado | Arquitetura desenhada em [`docs/arquitetura-alvo.md`](arquitetura-alvo.md) com separação clara de CQRS. ADRs de 001 a 011 em [`docs/adrs`](adrs/). |
| CH-04 | Requisitos funcionais e não funcionais | Verificado | Fluxos BDD (`.feature`) na pasta [`features`](../features) garantindo o funcionamento do negócio ponta a ponta. |
| CH-05 | Arquitetura alvo completa | Verificado | Detalhada no diagrama do [`README.md`](../README.md) e em [`docs/arquitetura-alvo.md`](arquitetura-alvo.md). Stack de infraestrutura em [`docker-compose.yaml`](../docker-compose.yaml). |
| CH-06 | Justificativas das decisões e tecnologias | Verificado | As 11 Decisões de Arquitetura (ADRs) documentam os prós e contras na pasta [`docs/adrs/`](adrs/). |
| CH-07 | Implementação em linguagem dominada | Verificado | Código 100% Go 1.26.5 com Clean Architecture, gRPC, Protobuf e integração com Postgres/RabbitMQ. |
| CH-08 | Testes | Verificado | Integração com Testcontainers em [`test/integration`](../test/integration) e E2E via Godog (`test/bdd`). Rodados via `make ci`. |
| CH-09 | README com funcionamento e execução | Verificado | [`README.md`](../README.md) raiz reescrito com instruções claras de subida do ambiente. |
| CH-10 | Repositório público GitHub | Verificado | Repositório e histórico blindados contra vazamentos de credenciais (Gitleaks). |
| CH-11 | Toda documentação no repositório | Verificado | Esta pasta `docs/` é a fonte única de verdade técnica do projeto. |
