# language: pt
@RF-04 @critico
Funcionalidade: Consolidar lançamentos de forma assíncrona
  Como comerciante
  Quero que meus lançamentos formem um resumo diário
  Para acompanhar o fluxo de caixa sem bloquear novos registros

  @SCN-RF04-001
  Cenário: Aplicar um lançamento confirmado
    Dado que existe um lançamento confirmado e ainda não aplicado
    Quando ele for processado pelo consolidado
    Então os totais do comerciante e da data de negócio devem ser atualizados
    E o lançamento deve produzir exatamente um efeito financeiro

  @SCN-RF04-002
  Cenário: Receber novamente um lançamento já aplicado
    Dado que o lançamento já produziu seu efeito no consolidado
    Quando a mesma atualização for entregue novamente
    Então os totais e o saldo devem permanecer inalterados

  @SCN-RF04-003
  Cenário: Declarar lacuna quando um lançamento chegar fora de ordem
    Dado que a posição 1 do comerciante foi aplicada
    E que a fonte do comerciante declarou a posição 3
    Quando a posição 3 chegar antes da posição 2
    Então nenhuma atualização deve ser perdida
    E "source_position" deve ser 3
    E "applied_position" contígua deve permanecer 1
    E o consolidado deve permanecer não definitivo

  @SCN-RF04-004
  Cenário: Fechar a lacuna de posições do comerciante
    Dado que as posições 1 e 3 do comerciante já foram aplicadas
    E que "source_position" do comerciante é 3
    E que "applied_position" contígua do comerciante é 1
    Quando a posição 2 do comerciante for entregue e aplicada
    Então "applied_position" deve avançar para 3
    E o consolidado deve tornar-se atualizado e definitivo

  @SCN-RF04-005
  Cenário: Aplicar lançamento retroativo
    Dado que existe um lançamento confirmado para uma data dentro dos últimos 30 dias
    Quando ele for aplicado
    Então os totais dessa data devem ser corrigidos
    E os saldos acumulados posteriores devem ser recompostos
    E o intervalo afetado deve permanecer não definitivo até o fim da recomposição

  @SCN-RF04-006
  Cenário: Consolidar estorno
    Dado que existe uma compensação confirmada
    Quando ela for processada
    Então deve afetar os totais da data do estorno
    E não deve reescrever os totais da data do lançamento original

