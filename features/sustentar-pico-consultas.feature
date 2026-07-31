# language: pt
@RNF-03 @carga
Funcionalidade: Sustentar o pico de consultas do consolidado
  Como responsável pela operação
  Quero validar a capacidade de consulta
  Para atender os dias de pico dentro do limite acordado

  @SCN-RNF03-001 @evidencia-k6
  Cenário: Executar carga sustentada de consultas válidas
    Dado que o ambiente passou por 30 segundos de aquecimento
    E que o aquecimento não participa das métricas de aceite
    E que ambiente, massa, distribuição de comerciantes, datas e estados de cache estão versionados
    Quando forem iniciadas 15.000 chegadas em modelo de taxa aberta a 50 consultas por segundo durante 5 minutos
    Então pelo menos 14.250 consultas devem retornar resposta HTTP 2xx em até 1 segundo
    E cada resposta bem-sucedida deve respeitar o contrato e conter o saldo esperado para sua massa
    E o percentil 95 calculado sobre todas as respostas concluídas deve ser menor ou igual a 500 milissegundos
    E erros, timeouts e percentil 99 devem ser registrados separadamente
    E iterações não iniciadas ou descartadas pelo gerador devem ser reportadas e não contar como sucesso

