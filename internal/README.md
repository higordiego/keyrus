# Código interno

Código compartilhado entre serviços, não exposto fora do módulo Go (reforçado pelo compilador):

- `platform/auth`, `platform/edge`, `platform/grpcsecurity`, `platform/identityruntime`, `platform/tenancy`: identidade, escopos, isolamento multi-tenant e segurança de transporte gRPC, usados por Ledger e Consolidation.
- `platform/observability`, `platform/runtimeobs`: métricas e observabilidade de runtime compartilhadas entre os serviços.
- `bddcatalog`, `bddguard`, `bddrunner`, `bddsource`: o harness de BDD/Godog deste repositório, com catálogo de cenários, guarda estática contra steps vazios/no-op, e o runner que liga o manifesto aos bindings reais.
- `postgrestest`: infraestrutura de teste compartilhada (Testcontainers) para os testes de integração contra PostgreSQL real.

A lógica de negócio específica de cada domínio (Ledger, Consolidation) mora em `services/*/internal/`, não aqui.
