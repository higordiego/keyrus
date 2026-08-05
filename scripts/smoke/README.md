# Smoke tests

Execute contra uma stack do `docker compose` real e já construída (ou deixe o
`clean-startup.sh` construí-la). Cada script utiliza `set -eu`: ele sai com erro não-zero (exits non-zero) no
primeiro teste que falhar, com uma mensagem explicando o que falhou.

| Script | Prova |
| --- | --- |
| [`clean-startup.sh`](clean-startup.sh) | `docker compose up --build` em uma máquina sem estado prévio (no prior state) (sem volumes) alcança health total com zero passos manuais, incluindo que `ledger-migrate`/`consolidation-migrate` realmente rodaram e saíram com código 0 (exited 0), que é o que faz a autenticação do Postgres de qualquer outro serviço funcionar afinal. |
| [`restart-isolation.sh`](restart-isolation.sh) | Parar cada container exclusivo de Consolidation não afeta a prontidão (readiness) do Ledger ou a sua habilidade de aceitar uma requisição (T10 Aceite). |
| [`replica-loss.sh`](replica-loss.sh) | Matar e reiniciar uma única instância de serviço stateless se recupera perfeitamente sem corromper ou degradar o resto da stack. Esta stack Compose não possui serviços multi-réplica (isso é exclusivo do Swarm, fora do escopo aqui), então isto prova a propriedade de statelessness subjacente da qual a real replica loss depende, não o failover multi-réplica em si. |

## Não implementado: cache fallback

O quarto smoke test que o ticket T10 nomeia é cache-fallback-on-Redis-outage. Não há cache/Redis neste sistema (a T07 não construiu um; veja o painel Cache no dashboard do Grafana). Um script aqui não teria nada contra o que testar; não é substituído por um falso "pass".

## Executando

```sh
./scripts/smoke/clean-startup.sh       # também compila/inicia a stack
./scripts/smoke/restart-isolation.sh   # requer a stack já em pé
./scripts/smoke/replica-loss.sh        # requer a stack já em pé
```

Ou todos os três via `make smoke`.
