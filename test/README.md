# Testes

Este diretório concentra o harness executável. A documentação prática está separada para não duplicar os cenários:

- [Estratégia, execução e evidências](../docs/testing-strategy.md)
- [Rastreabilidade dos 81 cenários](../docs/testing-traceability.md)
- [Features oficiais](../features/)

## Estado atual

- 14 arquivos `.feature` e 81 tags únicas `@SCN-*` são parseados e inventariados.
- O inventário observado pelo Godog é reconciliado com o mesmo conjunto de 81 tags do catálogo.
- `TestImplementedScenarios` conecta `steps.Initialize`, `bddguard.ValidateStepSources` e `bddrunner.Run`.
- Fixtures de política provam que binding real com efeito observável passa, enquanto tag sem binding, manifesto/tag inválida, `pending`, `skip` e handlers triviais por `Step`/`Given`/`When`/`Then` falham.
- O catálogo é deliberadamente plano nesta versão: um bloco `Regra:` é rejeitado com erro preciso, nunca descartado silenciosamente.
- `features/implemented_scenarios.txt` está vazio: **0 cenários de negócio estão implementados**.
- Cenários fora do manifesto são especificações aprovadas, não testes executados nem evidência de implementação.
- `undefined`, `pending`, `ambiguous` ou `skipped` não podem contar como sucesso quando um cenário entrar na suíte implementada.

## Verificação rápida

```sh
make bdd-parse
go test -race ./test/bdd/...
```

O resultado esperado neste estágio é `14 features`, `81 unique scenarios` e `0 implemented`. O manifesto vazio é submetido ao runner e sua seleção vacuosa precisa ser rejeitada como esperado pelo teste. Assim, um `PASS` comprova catálogo, parsing, reconciliação Godog–catálogo e política não vacuosa do harness; ele não prova comportamento financeiro, persistência, mensageria nem integração dos serviços.

`make reports` gera `evidence/reports/bdd-catalog.json` e `evidence/reports/go-test.json`; o CI publica esses arquivos como artefato. Eles são saídas transitórias ignoradas pelo Git e não estão presentes em um clone até a geração.

## Registro aceito de steps

O guard resolve handlers considerando todos os arquivos do mesmo package e falha fechado com `cannot be resolved within its package` quando não consegue provar o corpo executado. Nesta versão, registre cada step por referência direta a uma função local ou a um método local de nome unívoco; funções inline com efeito/asserção observável também são aceitas.

Selectors cujo receptor é um package importado não são confundidos com métodos locais homônimos: são rejeitados como externos irresolvíveis. Métodos de mesmo nome em receptores diferentes e factories como `makeHandler(dep)` não são suportados pela análise AST atual. Use nomes unívocos e referências diretas; se esses padrões se tornarem necessários, o guard deverá evoluir com análise de tipos em ticket posterior.
