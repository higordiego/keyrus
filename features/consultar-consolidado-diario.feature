# language: pt
@RF-05
Funcionalidade: Consultar o consolidado diário
  Como comerciante autenticado
  Quero consultar uma data ou período
  Para conhecer meu movimento diário e saldo acumulado

  @SCN-RF05-001
  Cenário: Consultar um dia atualizado
    Dado que não existe pendência conhecida até a posição declarada
    Quando o comerciante consultar a data
    Então deve receber créditos, débitos, líquido, quantidade e saldo acumulado
    E deve receber o instante do snapshot
    E deve receber "source_position" e "applied_position"
    E as duas posições devem ser compatíveis no corte declarado
    E o estado deve ser "atualizado"
    E o valor deve estar marcado como definitivo

  @SCN-RF05-002
  Cenário: Consultar um dia sem movimentações
    Dado que não existem lançamentos na data consultada
    E que existe saldo acumulado do dia anterior
    Quando o comerciante consultar a data
    Então créditos, débitos, líquido e quantidade devem ser zero
    E o saldo acumulado deve carregar o fechamento anterior

  @SCN-RF05-003
  Cenário: Consultar um período
    Dado que existem consolidados dentro e fora do período informado
    Quando o comerciante consultar datas inicial e final válidas
    Então deve receber um resultado por dia dentro do período
    E cada dia deve informar seu próprio estado de atualização

  @SCN-RF05-004
  Cenário: Consultar saldo em processamento
    Dado que existe atualização pendente há no máximo 30 segundos
    E que existe um snapshot anterior válido
    Quando o comerciante consultar a data afetada
    Então deve receber o último snapshot conhecido
    E o estado deve ser "processando"
    E o valor deve estar marcado como não definitivo
    E "source_position" deve estar à frente de "applied_position" ou existir uma lacuna declarada

  @SCN-RF05-005
  Cenário: Consultar saldo atrasado
    Dado que existe atualização pendente há mais de 30 segundos
    E que existe um snapshot anterior válido
    Quando o comerciante consultar a data afetada
    Então deve receber o último snapshot conhecido
    E o estado deve ser "atrasado"
    E o valor deve estar marcado como não definitivo
    E a resposta deve informar "source_position" e "applied_position"

  @SCN-RF05-006
  Cenário: Consultar data afetada por DLQ
    Dado que existe uma atualização persistente em tratamento de erro para a data
    Quando o comerciante consultar a data afetada
    Então o estado deve ser "atrasado"
    E o valor deve estar marcado como não definitivo
    E o sistema não deve ocultar a lacuna conhecida

  @SCN-RF05-007
  Cenário: Consultar enquanto ainda não existe snapshot
    Dado que a data possui atualização pendente
    E que nenhum snapshot foi produzido para ela
    Quando o comerciante consultar a data
    Então a resposta HTTP deve ser bem-sucedida
    E o campo de dados consolidados deve ser nulo
    E o estado deve ser "processando" ou "atrasado" conforme a idade da pendência
    E não deve apresentar zero como saldo definitivo

  @SCN-RF05-008
  Cenário: Não conseguir acessar um snapshot válido
    Dado que nenhum snapshot pode ser acessado ou comprovado
    Quando o comerciante consultar a data
    Então a consulta deve falhar explicitamente como indisponível
    E nenhum saldo deve ser apresentado como válido

  @SCN-RF05-009
  Cenário: Detectar lançamento confirmado ainda não publicado
    Dado que o RPC "GetMerchantWatermark" da Ledger informa "source_position" 4 para o comerciante
    E que o consolidado possui "applied_position" 3
    E que o evento da posição 4 ainda não foi entregue
    Quando o comerciante consultar a data afetada
    Então a resposta deve informar "source_position" 4 e "applied_position" 3
    E o valor deve estar marcado como não definitivo
    E o estado deve ser "processando" ou "atrasado" conforme a idade da pendência

  @SCN-RF05-010
  Cenário: Servir snapshot sem alegar frescor quando a fonte não puder ser verificada
    Dado que existe um snapshot persistido do comerciante
    E que a chamada gRPC de watermark da Ledger está indisponível
    Quando o comerciante consultar a data
    Então deve receber o último snapshot persistido
    E o estado deve ser "atrasado"
    E o valor deve estar marcado como não definitivo
    E o motivo deve ser "source_unverifiable"

