# Runbook: Cache (Redis) indisponível

## Status atual: nenhum cache está implantado

Este runbook foi escrito antes da camada de cache que a tarefa T07 foi escopada para construir, porque um runbook que só é escrito *depois* do primeiro incidente é um runbook que chega tarde demais. A partir desta sessão, `services/consolidation` não possui cliente Redis, nenhum serviço `redis` no `docker-compose.yaml`, e nenhuma configuração relacionada a cache em qualquer lugar da base de código -- `docs/compliance-matrix.md` e o painel "Cache" do dashboard T09 dizem isso explicitamente em vez de fingir o contrário.

Não atue sobre um alerta ou painel de dashboard que pareça referenciar uma métrica de cache (`redis_*`, `cache_hit_ratio`, etc.) -- nenhuma dessas métricas está conectada a nada real ainda; se uma aparecer, é uma regressão na configuração de observabilidade, não um incidente de cache.

## O que este runbook vai cobrir quando um cache existir

`@SCN-RNF07-006` ("Consultar durante indisponibilidade do cache") já especifica o comportamento necessário: uma query de saldo deve retornar os mesmos valores e posições da própria persistência da projeção Consolidated quando o cache estiver indisponível, a query nunca deve tratar o cache como uma dependência de prontidão obrigatória, e uma métrica de fallback deve ser incrementada. Quando esse cenário tiver um binding real (ele ainda não tem -- veja `features/recuperar-erros-sem-saldo-incorreto.feature` e o manifesto BDD), este runbook deve ser preenchido com:

1. Como confirmar se o cache está realmente fora do ar (`up{job="redis"} == 0` ou equivalente) versus o caminho de fallback simplesmente sendo exercitado sob eviction normal do cache.
2. Se a taxa de fallback (`cache miss` / `cache fallback to PostgreSQL`, conforme a tabela de métricas mínimas do plano técnico) está dentro da faixa esperada para o tráfego atual, ou alta o suficiente para indicar que o próprio cache precisa de atenção, mesmo que a correção (correctness) não seja afetada.
3. A ação segura para um cache degradado, mas não fora do ar (ex. tempestade de eviction, pressão de memória) versus um totalmente inacessível.
4. Que nenhuma ação é necessária para proteger a correção durante a indisponibilidade -- o RNF07-006 garante que o Postgres é a fonte autoritativa e sempre responde -- apenas para restaurar o desempenho para o qual o cache existe.

## Condição de encerramento (uma vez implementado)

Cache acessível novamente, taxa `cache_fallback_total` de volta à linha de base, `up{job="redis"} == 1` por 2 minutos consecutivos.
