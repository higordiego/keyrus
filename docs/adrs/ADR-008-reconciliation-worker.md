# ADR-008: Provas Matemáticas com Reconciliation Worker

## O Problema
Sistemas assíncronos baseados em RabbitMQ/Filas inevitavelmente enfrentarão anomalias ao longo de meses operando em altíssima escala. Mensagens na Dead Letter Queue (DLQ), partições de rede que quebram transações na metade, ou apenas indisponibilidades temporárias. Sem uma rotina ativa, o saldo consolidado de um comerciante fatalmente ficaria defasado, e não haveria como garantir sua acurácia além da "esperança" de que o RabbitMQ transportou tudo.

## A Decisão
Criamos o **Reconciliation Worker**.
Trata-se de um worker executado periodicamente que paralisa um instante no tempo (watermark / cut), extrai o sumário matemático (quantos créditos e débitos exatos existem na origem) da **Ledger API** usando gRPC Streams, e compara os mesmos dados com a tabela final na **Consolidation API**. 

O Worker audita as faltas, extras e discrepâncias (gaps), e, usando *Advisory Locks* e *Compare-and-set* diretamente no banco de leitura, aplica as compensações matemáticas.

## Consequências Positivas
* Integridade absoluta garantida matematicamente. Nenhuma anomalia no barramento de eventos ou na rede afetará o saldo permanentemente.
* Não é preciso varrer e cruzar o banco de dados inteiro todos os dias. O *watermark* funciona de forma progressiva.
* Prova material aos auditores de que a consistência eventual "de fato aconteceu".

## Consequências Negativas
* O worker adiciona tráfego extra de leitura (Streams) ao longo de janelas em background.
* Complexidade no gerenciamento de *locks* e de "Leader Election", para impedir que dois workers de reconciliação atuem sobre a conta do mesmo comerciante ao mesmo tempo.

## Gatilhos de Revisão
* Se o volume de transações por hora se aproximar das dezenas de milhões e os recursos necessários para varrer o `StreamEntriesAtCut` se tornarem financeiramente inviáveis para rodar a cada hora, a janela de reconciliação deverá ser ajustada para ocorrer apenas à noite.
