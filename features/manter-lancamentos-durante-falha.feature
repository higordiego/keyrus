# language: pt
@RNF-01 @RNF-02 @critico
Funcionalidade: Manter lançamentos disponíveis quando o consolidado falhar
  Como comerciante
  Quero continuar registrando meu caixa durante falhas do consolidado
  Para não perder operações financeiras

  @SCN-RNF01-001
  Cenário: Consolidado totalmente indisponível
    Dado que todos os componentes exclusivos do consolidado estão interrompidos
    E que a fonte autoritativa de lançamentos está saudável
    Quando o comerciante registrar um lançamento válido
    Então o lançamento deve ser confirmado de forma durável
    E a prontidão do serviço de lançamentos deve permanecer saudável
    E nenhuma chamada síncrona ao consolidado deve ser necessária
    E a atualização deve permanecer pendente para processamento posterior

  @SCN-RNF01-002
  Cenário: Transporte assíncrono indisponível
    Dado que o transporte de atualizações está indisponível
    E que a fonte autoritativa de lançamentos está saudável
    Quando o comerciante registrar um lançamento válido
    Então o lançamento deve ser confirmado de forma durável
    E sua atualização deve permanecer recuperável

  @SCN-RNF01-003
  Cenário: Fonte autoritativa indisponível
    Dado que não é possível preservar o lançamento de forma durável
    Quando o comerciante tentar registrar um lançamento
    Então a solicitação deve ser rejeitada sem confirmação
    E o sistema não deve alegar que o lançamento foi aceito

  @SCN-RNF02-001
  Cenário: Recuperar lançamentos confirmados durante a queda
    Dado que o consolidado ficou interrompido por 2 minutos
    E que foram confirmados 6.000 lançamentos a 50 lançamentos por segundo durante a interrupção
    E que o teste guardou independentemente cada identificador confirmado
    E que o teste guardou contagem, soma de créditos e soma de débitos por comerciante e data
    E que foi fixada a posição final da fonte usada como corte
    Quando consumidor e dependências do consolidado ficarem "Ready"
    Então em até 5 minutos, backlog e DLQ relacionados devem ficar vazios e a reconciliação deve estar persistida com sucesso no corte fixado
    E todos os 6.000 identificadores confirmados devem continuar consultáveis na fonte oficial
    E nenhum identificador confirmado deve estar ausente
    E nenhum identificador adicional deve existir no corte comparado
    E contagem, créditos e débitos do consolidado devem ser iguais ao oráculo guardado
    E cada identificador deve produzir exatamente um efeito financeiro

  @SCN-RNF01-004
  Cenário: Preservar lançamento e outbox após queda antes da confirmação do broker
    Dado que o lançamento e seu item de outbox pendente foram confirmados na mesma transação durável
    E que o publicador foi bloqueado depois de enviar a mensagem e antes de receber a confirmação do broker
    Quando o processo do publicador for interrompido abruptamente
    Então todos os identificadores confirmados devem continuar consultáveis na fonte oficial
    E o item de outbox deve continuar pendente ou elegível para nova publicação

  @SCN-RNF02-002
  Cenário: Recuperar publicação após queda na janela commit-confirm
    Dado que existe um lançamento confirmado com item de outbox ainda pendente
    E que identificador, valor, data e posição do comerciante foram guardados pelo teste
    E que o transporte está saudável
    Quando o publicador reiniciado ficar "Ready" e concluir a publicação
    Então todos os identificadores devem alcançar o consolidado
    E a reconciliação no corte guardado deve indicar zero ausentes, zero extras e zero duplicados
