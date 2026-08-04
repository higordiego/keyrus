# ADR-005: Idempotência Nativa e Obrigatoriedade

## O Problema
No sistema antigo, se a internet de um comerciante caísse bem na hora de salvar um lançamento, ele não sabia se o lançamento tinha sido computado. Se ele clicasse no botão novamente, acabava debitando ou creditando o cliente duas vezes. O Consolidado então passava a exibir um saldo irreal.

## A Decisão
A `Idempotency-Key` passou a ser um campo **obrigatório** nos contratos Protobuf e no cabeçalho HTTP de qualquer requisição de alteração de estado no sistema (ex: registro de lançamento). 

A própria Ledger API faz um `INSERT` na tabela `idempotency_record` na mesmíssima transação do banco que insere o lançamento financeiro. Como o campo da chave de idempotência possui uma *Constraint Unique* (restrição de exclusividade no banco de dados), a segunda requisição com a mesma chave falhará no banco de dados e o serviço apenas retornará os dados da primeira requisição de forma limpa.

## Consequências Positivas
* Resolvemos matematicamente e em definitivo as duplicações acidentais.
* O comerciante ou aplicativo móvel pode retentar um lançamento quantas vezes quiser sem medo.

## Consequências Negativas
* A complexidade da base de dados cresce, já que além de salvar o lançamento financeiro, precisamos salvar os metadados de idempotência e mantê-los por um certo período.
* É necessário uma rotina de limpeza (*garbage collection*) para não guardar chaves de idempotência para sempre e estourar o disco.

## Gatilhos de Revisão
* Esta decisão é estrutural. Uma revisão só ocorreria caso mudássemos o modelo relacional para um modelo de log particionado puro onde a checagem não fosse possível antes da inserção.
