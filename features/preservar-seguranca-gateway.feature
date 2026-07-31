# language: pt
@RNF-08 @fitness-test @gateway
Funcionalidade: Preservar segurança e isolamento através do API Gateway
  Como responsável pela plataforma
  Quero usar o KrakenD como única borda HTTP pública
  Para centralizar políticas sem acoplar a disponibilidade dos serviços

  @SCN-RNF08-001
  Cenário: Manter o registro disponível quando o consolidado cair
    Dado que o KrakenD possui ao menos uma réplica saudável
    E que todos os componentes exclusivos do consolidado estão indisponíveis
    E que Ledger API e sua fonte autoritativa estão saudáveis
    Quando um comerciante autenticado registrar um lançamento válido pela borda pública
    Então o KrakenD deve encaminhar somente para a Ledger API
    E o lançamento deve ser confirmado de forma durável
    E nenhuma chamada síncrona ao consolidado deve ocorrer

  @SCN-RNF08-002
  Esquema do Cenário: Rejeitar JWT inválido na borda
    Dado que o JWT está "<condição>"
    Quando uma rota pública protegida for chamada pelo KrakenD
    Então a borda deve rejeitar a solicitação sem encaminhá-la ao serviço

    Exemplos:
      | condição             |
      | ausente              |
      | expirado             |
      | com assinatura inválida |

  @SCN-RNF08-003
  Cenário: Rejeitar credencial inválida mesmo ao contornar a borda
    Dado que uma chamada alcançou diretamente a rede privada da Ledger API
    E que seu JWT não é válido para a operação
    Quando o serviço validar identidade, escopo e comerciante
    Então a operação deve ser rejeitada
    E nenhum lançamento deve ser confirmado

  @SCN-RNF08-004
  Cenário: Preservar cabeçalhos necessários no encaminhamento
    Dado que uma requisição pública válida contém "Authorization", "Idempotency-Key", "traceparent" e "tracestate"
    Quando o KrakenD encaminhá-la ao serviço responsável
    Então o serviço deve receber os quatro cabeçalhos sem alteração semântica

  @SCN-RNF08-005
  Cenário: Não repetir automaticamente comando POST
    Dado que a Ledger API confirmou o lançamento mas interrompeu a resposta
    Quando o KrakenD observar a falha do backend
    Então o gateway não deve realizar uma segunda invocação do comando POST
    E uma repetição feita pelo cliente deve depender da mesma "Idempotency-Key"

  @SCN-RNF08-006
  Cenário: Continuar atendendo após perda de uma réplica do gateway
    Dado que existem ao menos duas réplicas saudáveis do KrakenD no routing mesh
    Quando uma réplica for interrompida
    Então as novas requisições devem ser encaminhadas para outra réplica saudável
    E os serviços internos, RabbitMQ e bancos devem permanecer sem publicação direta externa

  @SCN-RNF08-007
  Cenário: Preservar autenticação após perda de uma réplica do Keycloak
    Dado que existem ao menos duas réplicas saudáveis do Keycloak em nós distintos
    E que elas usam descoberta "jdbc-ping" e PostgreSQL HA externo
    Quando uma réplica do Keycloak for interrompida
    Então os endpoints OIDC publicados pelo KrakenD devem continuar atendidos pela réplica saudável
    E as sessões persistidas não devem depender do disco local da réplica perdida

  @SCN-RNF08-008
  Cenário: Não expor superfícies administrativas do provedor de identidade
    Dado que o Keycloak está acessível internamente pela rede do Swarm
    Quando um cliente externo consultar administração, health ou métricas pela borda pública
    Então o KrakenD deve rejeitar ou não possuir rota para esses caminhos
    E somente os caminhos públicos necessários ao protocolo OIDC devem estar expostos

  @SCN-RNF08-009
  Cenário: Proteger o RPC autoritativo de watermark
    Dado que o RPC de watermark da Ledger pertence somente à rede interna gRPC
    Quando uma chamada sem identidade de serviço válida tentar consultá-lo
    Então a Ledger API deve rejeitar a chamada
    E o KrakenD não deve possuir rota pública para esse RPC

