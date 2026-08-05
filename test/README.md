# Testes

Este diretório concentra o harness executável. A documentação prática está separada para não duplicar os cenários:

- [Estratégia, execução e evidências](../docs/testing-strategy.md)
- [Rastreabilidade dos 81 cenários](../docs/testing-traceability.md)
- [Features oficiais](../features/)

## Estado atual

- 14 arquivos `.feature` e 81 tags únicas `@SCN-*` são parseados e inventariados.
- O inventário observado pelo Godog é reconciliado com o mesmo conjunto de 81 tags do catálogo e com 95 execuções/pickles após expandir 6 outlines em 20 linhas de exemplos.
- `TestImplementedScenarios` conecta `steps.Initialize`, `bddguard.ValidateStepSources` e `bddrunner.Run`.
- Fixtures de política provam que binding real com efeito/asserção externa passa, enquanto tag sem binding, manifesto/tag inválida, `pending`, `skip`, shadowing, selector importado, wrapper no-op, estado somente local e handlers triviais por `Step`/`Given`/`When`/`Then` falham.
- O catálogo é deliberadamente plano nesta versão: um bloco `Regra:` é rejeitado com erro preciso, nunca descartado silenciosamente.
- O manifesto liga 25 tags (T02, T04, T06) a evidência fresca do runtime real; os demais 56 cenários continuam pendentes.
- Cenários fora do manifesto são especificações aprovadas, não testes executados nem evidência de implementação.
- `undefined`, `pending`, `ambiguous` ou `skipped` não podem contar como sucesso quando um cenário entrar na suíte implementada.
- Múltiplas tags do manifesto usam a sintaxe OR legada do Godog v0.15.1 (`@A,@B`); um teste executa duas tags e impede seleção vazia por filtro incompatível.

## Verificação rápida

```sh
make bdd-parse
go test -race ./test/bdd/...
```

O resultado esperado é `14 features`, `81 unique scenarios` e `25 implemented` (30 cenários / 165 steps executados após expandir os outlines). A suíte Godog exige evidência íntegra, fresca e específica por cenário/caso/oráculo, emitida por Keycloak, KrakenD, imagens finais das APIs, Collector e fault backend reais; sem esse artefato ela própria executa o E2E. Isso não declara comportamento financeiro, persistência ou mensageria.

`make bdd-parse` valida o catálogo e imprime o resultado no console; não há mais um artefato JSON agregado (`bdd-catalog.json`/`go-test.json`) publicado pelo CI.

## Registro aceito de steps

O guard resolve handlers considerando todos os arquivos do mesmo package e falha fechado quando não consegue provar o corpo local executado. Nesta versão, registre cada step por referência direta a uma função local ou a um método local de nome unívoco; funções inline com efeito/asserção sobre estado externo também são aceitas. Closures guardadas em variáveis, imports e receivers não resolvidos são rejeitados, ainda que exista função ou método homônimo.

Selectors cujo receptor é um package importado não são confundidos com métodos locais homônimos: são rejeitados como externos irresolvíveis, inclusive quando o path é versionado e seu basename não é o nome declarado do package. Delegações locais diretas são percorridas, portanto wrappers no-op também falham. A análise é uma barreira sintática e não substitui o oráculo BDD executado. Métodos de mesmo nome em receptores diferentes e factories como `makeHandler(dep)` não são suportados; use nomes unívocos e referências diretas.
