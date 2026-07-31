# language: pt
@RNF-07 @observabilidade
Funcionalidade: Tornar falhas e atrasos observáveis
  Como responsável pela operação
  Quero correlacionar requisições, mensagens e consolidação
  Para detectar e diagnosticar perda de desempenho ou consistência

  @SCN-RNF07-001
  Cenário: Rastrear um lançamento ponta a ponta
    Dado que um lançamento foi confirmado
    Quando sua atualização percorrer o processamento assíncrono
    Então requisição, publicação, consumo e consolidação devem compartilhar o mesmo "trace_id"
    E devem registrar "entry_id", "merchant_id" pseudonimizado e "event_id" quando aplicáveis
    E traces e logs não devem conter token, chave de idempotência, descrição livre ou valor financeiro

  @SCN-RNF07-002
  Cenário: Alertar atraso do consolidado
    Dado que a atualização mais antiga está pendente há mais de 30 segundos
    Quando a condição for observada
    Então um alerta deve ser acionado
    E deve informar serviço, ambiente, idade do atraso e correlação operacional

  @SCN-RNF07-003
  Cenário: Alertar item em tratamento persistente
    Dado que existe pelo menos uma atualização em DLQ
    Quando a condição for observada
    Então um alerta deve permanecer ativo
    E só deve encerrar após reprocessamento e reconciliação bem-sucedidos

  @SCN-RNF07-004
  Cenário: Alertar taxa de erro acima do limite
    Dado que o consolidado está atendendo o perfil de pico
    Quando a taxa de falhas ultrapassar 5 por cento em uma janela móvel completa de 5 minutos
    Então um alerta deve ser acionado
    E a operação deve visualizar taxa de erro, latência e volume de requisições

  @SCN-RNF07-005
  Cenário: Avaliar prontidão de lançamentos durante falha do consolidado
    Dado que o consolidado está indisponível
    E que a fonte autoritativa de lançamentos está saudável
    Quando a prontidão do serviço de lançamentos for consultada
    Então ela deve permanecer saudável
    E a falha do consolidado deve aparecer apenas em sua própria saúde e nos alertas relacionados

  @SCN-RNF07-006
  Cenário: Consultar durante indisponibilidade do cache
    Dado que existe um consolidado atualizado e persistido
    E que o cache está indisponível
    Quando o comerciante consultar esse saldo
    Então deve receber os mesmos valores e posições a partir da persistência do consolidado
    E a consulta não deve apresentar o cache como dependência obrigatória de prontidão
    E uma métrica de fallback deve ser incrementada

