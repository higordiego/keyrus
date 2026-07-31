# language: pt
@RF-02
Funcionalidade: Consultar lançamentos próprios
  Como comerciante autenticado
  Quero localizar meus lançamentos
  Para conferir a origem do meu saldo

  @SCN-RF02-001
  Cenário: Consultar um lançamento pelo identificador
    Dado que existe um lançamento pertencente ao comerciante autenticado
    Quando ele consultar esse identificador
    Então deve receber tipo, valor, data, descrição, estado e referências de estorno

  @SCN-RF02-002
  Cenário: Filtrar lançamentos por período
    Dado que existem lançamentos em diferentes datas
    Quando o comerciante informar data inicial e final válidas
    Então deve receber somente seus lançamentos dentro do período
    E as duas datas do filtro devem ser inclusivas
    E os itens devem estar ordenados por data de negócio, confirmação e identificador em ordem decrescente
    E deve receber um cursor opaco quando houver próxima página

  @SCN-RF02-003
  Cenário: Usar o limite padrão de paginação
    Dado que existem mais de 100 lançamentos dentro do período
    Quando o comerciante consultar sem informar limite
    Então deve receber no máximo 50 itens

  @SCN-RF02-004
  Cenário: Rejeitar limite de paginação acima do máximo
    Dado que existem lançamentos dentro do período
    Quando o comerciante consultar com limite maior que 100
    Então a solicitação deve ser rejeitada

  @SCN-RF02-005
  Cenário: Paginar enquanto novos lançamentos são confirmados
    Dado que o comerciante recebeu a primeira página com um cursor que fixa o high-water mark da consulta
    E que novos lançamentos foram confirmados depois dessa resposta
    Quando solicitar a próxima página com o cursor recebido
    Então nenhum item existente até o high-water mark deve ser repetido ou omitido
    E os lançamentos posteriores ao high-water mark não devem aparecer nessa travessia

  @SCN-RF02-006
  Cenário: Consultar lançamento de outro comerciante
    Dado que o identificador pertence a outro comerciante
    Quando o comerciante autenticado tentar consultá-lo
    Então o acesso deve ser negado sem revelar a existência do recurso

