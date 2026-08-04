# ADR-001: Separação de Gravação e Leitura (CQRS)

## O Problema
No sistema legado, quando um comerciante criava um lançamento, o sistema usava a mesma transação no banco de dados para salvar a transação e calcular o novo saldo total. Com o crescimento dos dados, calcular o saldo de comerciantes com milhões de lançamentos começou a demorar muito (mais de 45 segundos), gerando bloqueios gigantes no banco de dados. Isso fazia com que novos lançamentos dessem timeout, impedindo os comerciantes de operarem.

## A Decisão
Adotamos o padrão **CQRS** (Command Query Responsibility Segregation). 

Nós quebramos a aplicação ao meio:
1. **Ledger API (Command):** Só aceita registros de débito e crédito. A única responsabilidade dela é validar a idempotência e carimbar a transação em um banco de dados otimizado apenas para adição (append-only). Ela NUNCA calcula saldos e NUNCA espera pela leitura.
2. **Consolidation API (Query):** É a dona do saldo calculado. Ela responde instantaneamente qual é o saldo daquele comerciante no dia, sem recalcular tudo do zero.

## Consequências Positivas
* **Imunidade a timeouts de leitura:** Se a Consolidation API cair ou demorar, a Ledger API continua 100% funcional, aceitando vendas sem interrupção.
* **Escalabilidade independente:** Podemos escalar a leitura separadamente da escrita, focando nos gargalos específicos de cada uma.
* **Resiliência:** A transação do Ledger fica minúscula (milissegundos), acabando com os "locks" prolongados no banco de dados.

## Consequências Negativas
* **Consistência Eventual:** Ao separar os dois lados, o sistema não é mais perfeitamente em tempo real. Há uma janela de milissegundos a segundos entre o registro na Ledger e o reflexo do saldo no Consolidado.
* **Maior complexidade na infraestrutura:** Precisamos de um meio para levar a informação de um lado para o outro de forma garantida.

## Alternativas Consideradas
* **Otimizar as Queries do Legado:** Criar índices e Materialized Views no banco atual. Foi descartado pois o volume de crescimento logo invalidaria os índices, e a concorrência na escrita do saldo continuaria existindo.

## Gatilhos de Revisão
* Se o tempo de sincronização entre a gravação e a atualização do saldo começar a ultrapassar sistematicamente 30 segundos, gerando atrito no uso pelo comerciante, a estratégia de consistência eventual precisará ser revista.
