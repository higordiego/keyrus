# language: pt
@RF-04 @RF-05 @oraculo-financeiro @critico
Funcionalidade: Calcular resultados financeiros exatos
  Como comerciante
  Quero resultados numéricos determinísticos
  Para confiar que débitos, créditos, duplicatas, retroatividade e estorno foram aplicados corretamente

  @SCN-RF04A-001
  Cenário: Calcular saldos iniciais com valores exatos
    Dado que o saldo de abertura do comerciante é R$ 0,00
    E que a posição 1 é um crédito de R$ 100,00 em "2026-07-30"
    E que a posição 2 é um débito de R$ 30,00 em "2026-07-30"
    E que a posição 3 é um crédito de R$ 10,00 em "2026-07-31"
    Quando as posições 1, 2 e 3 forem aplicadas
    Então o dia "2026-07-30" deve possuir créditos de R$ 100,00
    E débitos de R$ 30,00
    E líquido de R$ 70,00
    E quantidade igual a 2
    E saldo acumulado de R$ 70,00
    E o dia "2026-07-31" deve possuir créditos de R$ 10,00
    E débitos de R$ 0,00
    E líquido de R$ 10,00
    E quantidade igual a 1
    E saldo acumulado de R$ 80,00
    E "source_position" e "applied_position" devem ser 3

  @SCN-RF04A-002
  Cenário: Ignorar duplicata sem alterar o resultado numérico
    Dado que as posições 1, 2 e 3 do comerciante já foram aplicadas
    E que o dia "2026-07-30" possui créditos de R$ 100,00, débitos de R$ 30,00, quantidade 2 e saldo acumulado de R$ 70,00
    E que o dia "2026-07-31" possui créditos de R$ 10,00, débitos de R$ 0,00, quantidade 1 e saldo acumulado de R$ 80,00
    Quando a posição 3 for entregue novamente
    Então os valores, quantidades e saldos dos dois dias devem permanecer inalterados
    E "source_position" e "applied_position" do comerciante devem permanecer 3

  @SCN-RF04A-003
  Cenário: Recompor saldos exatos após lançamento retroativo
    Dado que o saldo de abertura do comerciante é R$ 0,00
    E que a posição 1 é um crédito de R$ 100,00 em "2026-07-30"
    E que a posição 2 é um débito de R$ 30,00 em "2026-07-30"
    E que a posição 3 é um crédito de R$ 10,00 em "2026-07-31"
    E que as posições 1, 2 e 3 já foram aplicadas
    Quando a posição 4 do comerciante, um débito retroativo de R$ 20,00 em "2026-07-30", for aplicada
    Então o dia "2026-07-30" deve possuir créditos de R$ 100,00
    E débitos de R$ 50,00
    E líquido de R$ 50,00
    E quantidade igual a 3
    E saldo acumulado de R$ 50,00
    E o dia "2026-07-31" deve continuar com líquido de R$ 10,00
    E deve possuir saldo acumulado de R$ 60,00
    E "source_position" e "applied_position" devem ser 4

  @SCN-RF04A-004
  Cenário: Aplicar estorno na data corrente sem reescrever o histórico
    Dado que o fuso do comerciante é "America/Fortaleza"
    E que o relógio está fixado em "2026-08-01T12:00:00-03:00"
    E que as posições 1 a 4 do comerciante já foram aplicadas
    E que a posição 3 é um crédito de R$ 10,00 em "2026-07-31"
    E que o saldo acumulado em "2026-07-31" é R$ 60,00
    E que não existe outra movimentação em "2026-08-01"
    Quando a posição 5 estornar integralmente o crédito da posição 3
    Então o dia "2026-08-01" deve possuir créditos de R$ 0,00
    E débitos de R$ 10,00
    E líquido de R$ -10,00
    E quantidade igual a 1
    E saldo acumulado de R$ 50,00
    E os totais históricos de "2026-07-31" devem permanecer inalterados
    E "source_position" e "applied_position" do comerciante devem ser 5
