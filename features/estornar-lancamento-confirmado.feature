# language: pt
@RF-03 @critico
Funcionalidade: Estornar um lançamento confirmado
  Como comerciante autenticado
  Quero compensar um lançamento incorreto
  Para preservar a auditoria sem reescrever o histórico

  @SCN-RF03-001
  Cenário: Estornar integralmente um lançamento próprio
    Dado que existe um lançamento confirmado e ainda não estornado
    E que foi informada uma chave de idempotência inédita para o estorno
    E que o fuso do comerciante é "America/Fortaleza"
    E que o relógio está fixado em "2026-07-31T12:00:00-03:00"
    Quando o comerciante solicitar seu estorno
    Então deve ser criada uma movimentação de valor igual e tipo oposto
    E a compensação deve usar a data corrente do comerciante
    E deve referenciar o lançamento original
    E o lançamento original deve permanecer inalterado

  @SCN-RF03-002
  Cenário: Estornar lançamento ainda não consolidado
    Dado que o lançamento original está confirmado e pendente de consolidação
    Quando o comerciante solicitar o estorno
    Então o estorno deve ser confirmado sem depender da disponibilidade do consolidado

  @SCN-RF03-003
  Cenário: Repetir o estorno com a mesma chave e conteúdo
    Dado que um estorno foi confirmado com uma chave de idempotência
    Quando a mesma chave e o mesmo conteúdo forem enviados novamente
    Então deve ser devolvido o mesmo identificador de compensação
    E nenhum segundo efeito financeiro deve ocorrer

  @SCN-RF03-004
  Cenário: Reutilizar chave de estorno com conteúdo diferente
    Dado que uma chave de idempotência já está vinculada a um estorno
    Quando a mesma chave for usada para outro lançamento original
    Então a solicitação deve ser rejeitada como conflito
    E nenhum novo estorno deve ser confirmado

  @SCN-RF03-005
  Cenário: Concorrer com chaves diferentes pelo mesmo lançamento
    Dado que um lançamento ainda não foi estornado
    Quando duas solicitações válidas com chaves diferentes tentarem estorná-lo concorrentemente
    Então uma única compensação deve ser confirmada
    E a outra solicitação deve informar que o lançamento já foi estornado

  @SCN-RF03-006
  Cenário: Repetir estorno após timeout ocorrido depois da confirmação
    Dado que o estorno foi confirmado
    E que a resposta foi interrompida antes de chegar ao cliente
    Quando o cliente repetir a solicitação com a mesma chave e conteúdo
    Então deve receber o identificador da compensação originalmente criada
    E nenhum efeito financeiro adicional deve ocorrer

  @SCN-RF03-007
  Esquema do Cenário: Rejeitar estorno não permitido
    Dado que a tentativa é "<condição>"
    Quando o estorno for solicitado
    Então nenhuma compensação deve ser confirmada
    E o lançamento original deve permanecer inalterado

    Exemplos:
      | condição                         |
      | estorno parcial                  |
      | segundo estorno do mesmo lançamento |
      | estorno de outro estorno         |
      | lançamento de outro comerciante  |

