# language: pt
@RNF-09 @fitness-test @grpc
Funcionalidade: Padronizar a comunicação entre serviços
  Como responsável pela arquitetura
  Quero contratos internos gRPC e eventos assíncronos AMQP
  Para obter contratos fortes sem acoplar a confirmação de lançamentos

  @SCN-RNF09-001
  Cenário: Consultar watermark por gRPC autenticado
    Dado que a Consolidation Service possui certificado e JWT de serviço válidos
    Quando chamar "GetMerchantWatermark" na Ledger por gRPC com deadline
    Então a Ledger deve validar mTLS, audience, escopo e comerciante
    E deve devolver a posição confirmada no contrato Protobuf

  @SCN-RNF09-002
  Cenário: Degradar o relatório quando o gRPC de watermark expirar
    Dado que existe um snapshot persistido
    E que a chamada gRPC de watermark excede seu deadline
    Quando o comerciante consultar o consolidado pela borda
    Então o snapshot deve ser devolvido como "atrasado" e não definitivo
    E o motivo deve ser "source_unverifiable"
    E nenhum fallback HTTP interno deve ser tentado

  @SCN-RNF09-003
  Cenário: Reconciliar por stream gRPC sem acessar o banco da Ledger
    Dado que o worker possui um corte e identidade de serviço válidos
    Quando solicitar "StreamEntriesAtCut" à Ledger
    Então os lançamentos até o corte devem ser transmitidos por server-streaming gRPC
    E o worker não deve possuir credencial nem conexão direta com o banco da Ledger

  @SCN-RNF09-004
  Cenário: Propagar contexto e limites na chamada gRPC
    Dado que uma chamada interna possui trace context e deadline
    Quando atravessar um cliente e servidor gRPC
    Então "traceparent" deve permanecer correlacionável em metadata
    E cancelamento, deadline e limite de tamanho devem ser respeitados
    E JWT, valores e descrições não devem aparecer em traces ou logs

  @SCN-RNF09-005
  Cenário: Manter HTTP apenas no adapter da borda Community
    Dado que o contrato Protobuf é a fonte do adapter público
    Quando o KrakenD Community encaminhar uma chamada HTTP e JSON
    Então o adapter gerado deve convertê-la para o mesmo caso de uso local
    E nenhuma chamada síncrona entre serviços deve usar HTTP

  @SCN-RNF09-006
  Cenário: Preservar RabbitMQ no fluxo assíncrono
    Dado que um lançamento foi confirmado com outbox pendente
    Quando sua atualização for encaminhada ao consolidado
    Então a comunicação deve usar evento AMQP persistente via RabbitMQ
    E nenhuma chamada gRPC ao consolidado deve participar da confirmação

