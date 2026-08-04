# ADR-002: Isolamento de Schemas no Banco de Dados

## O Problema
No sistema original, todos os dados habitavam o mesmo banco de dados sem restrições lógicas. Era possível fazer "JOINs" gigantescos entre informações de configuração, perfis de usuários, registros brutos de transações e saldos agregados. Isso tornava o sistema um grande monólito de dados: qualquer gargalo em uma tabela afetava o banco todo, e o acoplamento excessivo impedia a migração do código para microserviços reais.

## A Decisão
Decidimos que cada domínio de negócio deve possuir o seu próprio esquema isolado (`ledger`, `consolidation`, `identity`), protegido por *roles* e usuários distintos do PostgreSQL.

Um serviço não tem a menor ideia da existência da estrutura do banco do outro.
* A `Ledger API` só consegue ler e gravar no schema `ledger`.
* A `Consolidation API` só interage com o schema `consolidation`.

A única forma de cruzarem dados é conversarem pelas APIs ou filas.

## Consequências Positivas
* **Blindagem de Falhas:** O travamento nas tabelas do Consolidado jamais travará os registros do Ledger, pois as transações e as conexões físicas/lógicas são totalmente separadas.
* **Independência Tecnológica:** Se um dia o Consolidado precisar migrar para um banco de dados colunar ou NoSQL, o Ledger não sofrerá impacto algum.
* **Segurança Profunda:** O usuário do banco da leitura não possui credenciais para adulterar a fonte da verdade da gravação.

## Consequências Negativas
* **Custo inicial de migração:** É difícil desmembrar tabelas que antes faziam queries conjuntas. A carga de dados inicial precisa passar pelo fluxo assíncrono ou ser importada de modo especial.
* **Tolerância a Duplicação Limitada:** Sem "Foreign Keys" cruzadas, o modelo de leitura precisa ter dados razoavelmente isolados (não há consistência relacional forçada pelo banco).

## Alternativas Consideradas
* **Bancos Físicos Separados:** Subir três instâncias completas de PostgreSQL. Foi descartado temporariamente pelo alto custo de infraestrutura. Isolar em schemas dentro de um único cluster garante o encapsulamento lógico sem multiplicar agressivamente a fatura do provedor de nuvem.

## Gatilhos de Revisão
* Se a concorrência global (CPU/IO) do cluster único se tornar o principal gargalo da aplicação, o isolamento lógico precisará ser convertido em isolamento físico (Bancos RDS distintos).
