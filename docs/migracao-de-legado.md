# Migração do Legado: Índice

Esse documento foi dividido em quatro arquivos, na ordem em que a leitura faz mais sentido:

1. [**O Legado e os Incidentes Reais**](legado-e-incidentes.md): a macro arquitetura do monolito anterior e os quatro incidentes reais (deadlock, timeout de relatório, duplicidade, IDOR/voo cego) que motivaram a reescrita.
2. [**Arquitetura Alvo**](arquitetura-alvo.md): por que essa arquitetura, o diagrama completo do sistema (Ledger, Consolidado, borda, mensageria, observabilidade) e o caminho de uma requisição do começo ao fim.
3. [**Arquitetura de Transição**](arquitetura-de-transicao.md): o plano de migração do legado pro novo sistema, fase a fase (Strangler Fig), com rollback definido em cada etapa.
4. [**Defesa do Modelo**](defesa-arquitetural.md): a tabela de-para problema/mecanismo, e a jornada de implementação com os bugs reais que a revisão independente encontrou e corrigiu antes de cada integração.
