# Step definitions

Somente steps reais entram aqui. Ao ligar um cenário, adicione sua tag ao manifesto e seu binding; `undefined`, `pending`, `skip` e catch-all de sucesso fazem o gate falhar.

Os fixtures rápidos de T02 ainda exercitam contratos de identidade, política do
edge e gRPC, mas não contam como aceite BDD porque usam substitutos in-process.
O aceite de runtime está em `test/integration/runtime_e2e_test.go`, com Keycloak,
KrakenD e adapters reais. Uma tag T02 só deve voltar ao manifesto quando o
binding Godog dirigir esse mesmo runtime real.

Antes de registrar handlers, consulte a [estratégia de testes](../../../docs/testing-strategy.md), que documenta resolução package-wide, formatos aceitos e restrições fail-closed do guard.
