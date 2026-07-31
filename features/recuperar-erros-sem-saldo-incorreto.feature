# language: pt
@RNF-04 @critico
Funcionalidade: Recuperar erros sem publicar saldo incorreto
  Como responsável pela operação
  Quero isolar e reconciliar falhas persistentes
  Para restaurar o saldo com evidência auditável

  @SCN-RNF04-001
  Cenário: Isolar atualização com erro persistente
    Dado que uma atualização do dia "2026-07-30" falhou após o limite de tentativas
    E que existem saldos acumulados em "2026-07-31" e "2026-08-01"
    Quando ela for isolada para tratamento
    Então não deve bloquear atualizações independentes
    E deve permanecer como pendência financeira conhecida
    E os dias "2026-07-30", "2026-07-31" e "2026-08-01" devem ficar com estado "atrasado"
    E um alerta deve permanecer ativo

  @SCN-RNF04-002
  Cenário: Reprocessar atualização isolada com efeito financeiro exato
    Dado que uma atualização de crédito de R$ 25,00 na posição 4 do comerciante está na DLQ
    E que "source_position" do comerciante é 4 e "applied_position" contígua é 3
    E que a data da atualização e seus acumulados posteriores estão "atrasado"
    E que o saldo anterior à atualização é R$ 100,00
    E que existe um alerta ativo para essa pendência
    E que a causa do erro persistente foi corrigida
    Quando a atualização for reprocessada
    Então ela deve produzir exatamente um efeito financeiro de crédito de R$ 25,00
    E o saldo reconciliado deve ser R$ 125,00
    E "applied_position" do comerciante deve avançar para 4
    E o intervalo afetado deve ser reconciliado
    E backlog e DLQ relacionados devem ficar vazios
    E todos os dias afetados devem voltar a "atualizado"
    E o alerta só deve encerrar depois dessas condições e da reconciliação bem-sucedida

  @SCN-RNF04-003
  Cenário: Reconciliar contra um corte estável
    Dado que foi definida uma posição estável da fonte oficial
    Quando a reconciliação comparar lançamentos e consolidados até essa posição
    Então deve registrar contagens, valores e divergências por comerciante e data
    E deve corrigir o dia divergente e os acumulados posteriores
    E não deve sobrescrever uma versão mais nova
    E repetir a reconciliação para o mesmo corte não deve criar novos efeitos

