# language: pt
@RNF-05 @critico
Funcionalidade: Impedir duplicidade de lançamentos
  Como cliente da API de lançamentos
  Quero repetir uma solicitação com segurança
  Para recuperar falhas de comunicação sem duplicar valores

  @SCN-RNF05-001
  Cenário: Repetir a mesma solicitação com a mesma chave
    Dado que um lançamento foi confirmado com uma chave de idempotência
    Quando a mesma chave e o mesmo conteúdo forem enviados novamente
    Então a resposta original deve ser devolvida
    E nenhum novo lançamento deve ser criado
    E o saldo deve receber um único efeito financeiro

  @SCN-RNF05-002
  Cenário: Reutilizar a chave com conteúdo diferente
    Dado que uma chave de idempotência já está vinculada a um lançamento
    Quando a mesma chave for enviada com conteúdo diferente
    Então a solicitação deve ser rejeitada como conflito
    E o lançamento original deve permanecer inalterado

  @SCN-RNF05-003
  Cenário: Repetir após timeout ocorrido depois da confirmação
    Dado que o lançamento foi confirmado
    E que a resposta ao cliente foi interrompida antes de ser recebida
    Quando o cliente repetir a solicitação com a mesma chave e conteúdo
    Então deve receber o identificador originalmente criado
    E nenhum efeito financeiro adicional deve ocorrer

  @SCN-RNF05-004
  Cenário: Receber solicitações concorrentes com a mesma chave
    Dado que nenhuma resposta foi concluída para a chave informada
    Quando solicitações idênticas e concorrentes forem recebidas
    Então exatamente um lançamento deve ser confirmado
    E todas as respostas bem-sucedidas devem referenciar o mesmo identificador

