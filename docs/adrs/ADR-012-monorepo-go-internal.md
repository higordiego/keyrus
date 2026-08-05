# ADR-012: Monorepo Go com um único `go.mod` e fronteiras por `internal`

## O Problema
`ledger`, `consolidation` e o worker de reconciliação são domínios distintos, com times e ciclos de release potencialmente separados no futuro. Sem alguma fronteira de compilação, nada impede que `services/ledger` importe direto um tipo interno de `services/consolidation` (ou vice-versa), o acoplamento em nível de código voltaria a existir mesmo com bancos, roles e processos já isolados (ver [ADR-002](ADR-002-isolamento-de-bancos.md)). Ao mesmo tempo, `go mod`/módulos separados por serviço trazem custo real: versionamento cruzado de dependências compartilhadas (`internal/platform/*`, contratos gRPC gerados), `go.sum` triplicado, e builds/CI mais lentos e mais difíceis de manter consistentes.

## A Decisão
Um único módulo Go (`go.mod` na raiz) para o repositório inteiro, com as fronteiras de acoplamento impostas pelo compilador via pacotes `internal`:

- Cada serviço expõe seu código de domínio em `services/<serviço>/internal/...`, por regra da linguagem, **só** código sob `services/<serviço>/` pode importar isso; nenhum outro serviço, nem `test/...`, consegue.
- Código genuinamente compartilhado entre serviços (autenticação, observabilidade, gRPC security, redação de logs) vive em `internal/platform/...` na raiz, importável por qualquer serviço.
- Contratos entre serviços (proto gerado, o `Handler`/`Client` de `gen/go/cashflow/ledger/rpc`) ficam fora de `internal`, exatamente para serem o único ponto de acoplamento permitido entre `services/ledger` e `services/consolidation`.

Essa fronteira não é só convenção: apareceu na prática ao escrever o teste negativo de isolamento de roles do Postgres (T10) e um teste de regressão do handler gRPC interno do Ledger (T11), ambos precisaram ficar fisicamente dentro de `services/*/internal/...` (não em `test/integration`) porque o compilador rejeita a importação de um pacote `internal` alheio, mesmo de um pacote `_test` no mesmo módulo.

## Consequências Positivas
* Refatorar dentro de um serviço (renomear um tipo, mudar uma assinatura) nunca quebra outro serviço em tempo de compilação, a única superfície que outro serviço enxerga é o contrato gerado.
* Um `go build ./...`/`go vet ./...` na raiz cobre o monorepo inteiro; não existem três `go.sum` divergentes para manter sincronizados.
* A fronteira é verificada pelo compilador em todo `go build`/`go vet`/CI, não depende de revisão de código lembrar a regra.

## Consequências Negativas
* Um único `go.mod` significa uma única versão de cada dependência compartilhada para todos os serviços, não é possível, por exemplo, o Ledger atualizar `pgx` numa versão e o Consolidado ficar numa mais antiga por mais tempo.
* Qualquer teste que precise inspecionar o comportamento real de dois serviços ao mesmo tempo (ex.: o teste negativo de isolamento de roles do T10) tem que morar dentro do `internal` de um dos dois lados, o que é menos natural do que um pacote de teste de integração neutro em `test/`.
* Se algum serviço realmente precisar de um ciclo de release ou pipeline de CI independente no futuro, extrair um módulo Go separado exige mover código e reescrever imports, não é uma mudança de configuração trivial.

## Alternativas Consideradas
* **Um `go.mod` por serviço (multi-módulo).** Descartado por ora: o time é pequeno o suficiente para que o custo de sincronizar `go.sum`/versões entre módulos supere o benefício de release independente, que nenhum serviço precisa hoje.
* **Sem `internal`, só convenção de "não importe o pacote do outro serviço".** Descartado: convenção não verificada por ferramenta se rompe silenciosamente à primeira pressão de prazo; o objetivo explícito era fazer o compilador recusar o acoplamento, não só documentá-lo.

## Gatilhos de Revisão
* Se um serviço precisar de um cronograma de deploy, versão de dependência ou time de propriedade genuinamente independente dos demais, extrair esse serviço para seu próprio módulo/repositório deixa de ser opcional.
* Se o número de pacotes compartilhados em `internal/platform/` crescer a ponto de se tornar, na prática, um framework interno com API própria, vale reavaliar se ele merece um módulo Go isolado (versionado e testado à parte) em vez de viver dentro do monorepo.
