# language: pt
@RF-01 @critico
Funcionalidade: Registrar lançamentos financeiros
  Como comerciante autenticado
  Quero registrar créditos e débitos em BRL
  Para manter meu fluxo de caixa confiável

  Contexto:
    Dado que o comerciante está autenticado
    E que seu saldo de abertura é zero
    E que seu fuso configurado é "America/Fortaleza"

  @SCN-RF01-001
  Esquema do Cenário: Confirmar um lançamento válido no dia corrente
    Dado que foi informada uma chave de idempotência inédita
    E que o tipo é "<tipo>"
    E que o valor em BRL é positivo
    E que o relógio está fixado em "2026-07-31T12:00:00-03:00"
    Quando o comerciante solicitar o registro sem informar data de negócio
    Então o lançamento deve ser confirmado de forma durável
    E deve receber um identificador único
    E deve usar o dia corrente no fuso configurado do comerciante
    E sua consolidação deve ser tratada separadamente da confirmação

    Exemplos:
      | tipo    |
      | crédito |
      | débito  |

  @SCN-RF01-002
  Cenário: Registrar lançamento exatamente no limite retroativo
    Dado que o relógio está fixado em "2026-07-31T12:00:00-03:00"
    E que a data de negócio informada é "2026-07-01"
    E que foi informada uma chave de idempotência inédita
    Quando o comerciante solicitar o registro
    Então o lançamento deve ser confirmado na data informada
    E os consolidados dessa data até o dia corrente devem deixar de ser definitivos até a recomposição

  @SCN-RF01-003
  Esquema do Cenário: Rejeitar data de negócio inválida
    Dado que o relógio está fixado em "2026-07-31T12:00:00-03:00"
    E que a data informada é "<data>"
    Quando o comerciante solicitar o registro
    Então nenhum lançamento deve ser confirmado
    E a resposta deve explicar que a data está fora do intervalo permitido

    Exemplos:
      | data       |
      | 2026-08-01 |
      | 2026-06-30 |

  @SCN-RF01-004
  Cenário: Preservar a data histórica depois de alterar o fuso
    Dado que um lançamento foi confirmado em "2026-07-31" no fuso "America/Fortaleza"
    Quando o comerciante alterar seu fuso para "Europe/Lisbon"
    Então o lançamento deve continuar classificado na data "2026-07-31"

  @SCN-RF01-005
  Cenário: Classificar lançamento na virada do dia do comerciante
    Dado que o instante UTC é "2026-08-01T02:30:00Z"
    E que o fuso configurado é "America/Fortaleza"
    Quando o comerciante registrar um lançamento sem informar data de negócio
    Então a data de negócio deve ser "2026-07-31"

  @SCN-RF01-006
  Esquema do Cenário: Rejeitar dados financeiros inválidos
    Dado que o lançamento possui "<condição>"
    Quando o comerciante solicitar o registro
    Então nenhum lançamento deve ser confirmado
    E a resposta deve identificar o campo inválido

    Exemplos:
      | condição                  |
      | valor igual a zero        |
      | valor negativo            |
      | mais de duas casas decimais |
      | tipo diferente de crédito ou débito |
      | moeda diferente de BRL    |

