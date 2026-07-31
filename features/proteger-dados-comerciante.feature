# language: pt
@RNF-06 @seguranca
Funcionalidade: Proteger os dados de cada comerciante
  Como comerciante
  Quero que minha identidade determine o escopo das operações
  Para que outros comerciantes não acessem ou alterem meu caixa

  @SCN-RNF06-001
  Cenário: Acessar com identidade válida
    Dado que o token é válido e contém o comerciante e o escopo exigido
    Quando uma operação autorizada for solicitada
    Então o comerciante deve ser derivado da identidade autenticada
    E a operação deve permanecer limitada a esse comerciante

  @SCN-RNF06-002
  Esquema do Cenário: Rejeitar identidade inválida
    Dado que o token está "<condição>"
    Quando uma operação protegida for solicitada
    Então a solicitação deve ser rejeitada
    E nenhum dado financeiro deve ser alterado ou revelado

    Exemplos:
      | condição          |
      | ausente           |
      | expirado          |
      | com assinatura inválida |
      | sem o escopo exigido |

  @SCN-RNF06-003
  Cenário: Tentar acesso horizontal
    Dado que o recurso pertence a outro comerciante
    Quando a identidade autenticada tentar acessá-lo
    Então a operação deve ser negada
    E a resposta não deve revelar se o recurso existe
    E código e corpo devem seguir o mesmo contrato de um identificador inexistente
    E a análise estatística de tempo não deve permitir enumeração prática de recursos

  @SCN-RNF06-004
  Cenário: Isolar a mesma chave de idempotência entre comerciantes
    Dado que os comerciantes A e B estão autenticados separadamente
    E que ambos usam a chave "chave-compartilhada"
    Quando A registrar um crédito de R$ 100,00
    E B registrar um débito de R$ 30,00
    Então devem existir dois lançamentos independentes

  @SCN-RNF06-005
  Cenário: Consolidar independentemente comerciantes que usaram a mesma chave
    Dado que A possui um crédito confirmado de R$ 100,00
    E que B possui um débito confirmado de R$ 30,00
    E que ambos os lançamentos usaram a mesma chave de idempotência em seus respectivos escopos
    Quando as duas atualizações forem aplicadas pelo consolidado
    Então as posições de A e B devem avançar independentemente
    E o saldo de A deve ser R$ 100,00
    E o saldo de B deve ser R$ -30,00

  @SCN-RNF06-006
  Cenário: Isolar retroatividade e falha persistente entre comerciantes
    Dado que A e B possuem saldos atualizados para as mesmas datas
    E que uma atualização retroativa de A está isolada em DLQ
    Quando os estados dos dois comerciantes forem consultados
    Então os dias afetados de A devem ficar atrasados
    E lançamentos, posições, estado e saldo de B devem permanecer inalterados e atualizados

  @SCN-RNF06-007
  Cenário: Executar ação operacional privilegiada
    Dado que existe uma identidade operacional com privilégio mínimo
    Quando ela solicitar reprocessamento ou reconciliação
    Então a ação deve exigir escopo operacional específico
    E deve produzir registro auditável

