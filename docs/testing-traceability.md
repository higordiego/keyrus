# Rastreabilidade dos 81 cenários de teste

## Como ler

Os arquivos [`.feature`](../features/) são a fonte oficial. Esta matriz não repete passos; agrupa tags estáveis por capacidade e aponta o nível de teste e a evidência necessários.

Todos os grupos abaixo estão **aprovados e parseados**, mas **pendentes de implementação**, porque `features/implemented_scenarios.txt` ainda não possui tags.

## Matriz RF/RNF → SCN → teste → evidência

| RF/RNF | Fonte oficial e tags | Qtde. | Tipo primário | Evidência executável esperada |
|---|---|---:|---|---|
| RF-01 | [`registrar-lancamentos-financeiros.feature`](../features/registrar-lancamentos-financeiros.feature), `@SCN-RF01-001..006` | 6 | BDD de API + integração Ledger | relatório Godog, respostas, registro durável/outbox, relógio e fuso da fixture |
| RF-02 | [`consultar-lancamentos-proprios.feature`](../features/consultar-lancamentos-proprios.feature), `@SCN-RF02-001..006` | 6 | BDD de API/paginação | relatório Godog, páginas/cursor/high-water mark e respostas anti-enumeração |
| RF-03 | [`estornar-lancamento-confirmado.feature`](../features/estornar-lancamento-confirmado.feature), `@SCN-RF03-001..007` | 7 | BDD de API + concorrência | IDs original/compensação, respostas concorrentes, contagens e efeito financeiro |
| RF-04 | [`consolidar-lancamentos-assincronos.feature`](../features/consolidar-lancamentos-assincronos.feature), `@SCN-RF04-001..006` | 6 | integração de consumer | entregas controladas, deduplicação, posições, read model e estados |
| RF-04/RF-05 | [`calcular-resultados-financeiros-exatos.feature`](../features/calcular-resultados-financeiros-exatos.feature), `@SCN-RF04A-001..004` | 4 | domínio + integração com oráculo numérico | valores exatos por dia, posições e snapshots antes/depois |
| RF-05 | [`consultar-consolidado-diario.feature`](../features/consultar-consolidado-diario.feature), `@SCN-RF05-001..010` | 10 | BDD de API + consistência | respostas por estado, source/applied position, definitividade e fallback explícito |
| RF-06 (transversal) | `@SCN-RF01-002..005`, `@SCN-RF04-005`, `@SCN-RF04A-003`, `@SCN-RNF06-006` | subconjunto | datas, domínio e integração | limites D-30/D-31, fuso, recomposição numérica e isolamento por comerciante |
| RNF-01/RNF-02 | [`manter-lancamentos-durante-falha.feature`](../features/manter-lancamentos-durante-falha.feature), `@SCN-RNF01-001..004`, `@SCN-RNF02-001..002` | 6 | E2E/fault injection | IDs/somas independentes, outbox, backlog, readiness, corte e reconciliação |
| RNF-03 | [`sustentar-pico-consultas.feature`](../features/sustentar-pico-consultas.feature), `@SCN-RNF03-001` | 1 | desempenho k6 | summary k6, 15.000 chegadas, checks corretos, sucesso, p95/p99, erros e descartes |
| RNF-04 | [`recuperar-erros-sem-saldo-incorreto.feature`](../features/recuperar-erros-sem-saldo-incorreto.feature), `@SCN-RNF04-001..003` | 3 | integração DLQ/reconciliação | DLQ/backlog, estados multi-dia, efeito exato, posições, alerta e auditoria do corte |
| RNF-05 | [`impedir-duplicidade-lancamentos.feature`](../features/impedir-duplicidade-lancamentos.feature), `@SCN-RNF05-001..004` | 4 | integração/concurrency | respostas, mesmo ID, conflito, barreira concorrente, contagens e saldo final |
| RNF-06 | [`proteger-dados-comerciante.feature`](../features/proteger-dados-comerciante.feature), `@SCN-RNF06-001..007` | 7 | segurança + integração multi-tenant | tokens/claims, respostas, registros, saldos/posições A/B e auditoria operacional |
| RNF-07 | [`tornar-falhas-observaveis.feature`](../features/tornar-falhas-observaveis.feature), `@SCN-RNF07-001..006` | 6 | fitness de observabilidade | traces correlacionados/redigidos, métricas, alertas e evidência de fallback |
| RNF-08 | [`preservar-seguranca-gateway.feature`](../features/preservar-seguranca-gateway.feature), `@SCN-RNF08-001..009` | 9 | fitness de gateway/OIDC/HA | chamadas pela borda e rede privada, headers, failover, superfícies expostas e logs |
| RNF-09 | [`padronizar-comunicacao-servicos.feature`](../features/padronizar-comunicacao-servicos.feature), `@SCN-RNF09-001..006` | 6 | contrato + integração gRPC/AMQP | chamadas gRPC, deadlines/cancelamento, metadata, stream, evento RabbitMQ e ausência de fallback HTTP |
| **Total de cenários únicos** | **14 arquivos** | **81** |  | RF-06 referencia cenários já contados nas demais linhas |

## Visões transversais obrigatórias

| Tema | SCNs que formam o aceite | Complemento de evidência |
|---|---|---|
| Zero perda e queda do consolidado | `@SCN-RNF01-001..004`, `@SCN-RNF02-001..002`, `@SCN-RNF07-005`, `@SCN-RNF08-001` | oráculo independente de IDs/somas, outbox, readiness e reconciliação no mesmo corte |
| Idempotência de criação e estorno | `@SCN-RNF05-001..004`, `@SCN-RF03-003..006`, `@SCN-RF04A-002`, `@SCN-RNF06-004..005` | mesma chave nos escopos corretos, barreira concorrente, IDs e efeito final único |
| DLQ | `@SCN-RNF04-001..003`, `@SCN-RF05-006`, `@SCN-RNF06-006`, `@SCN-RNF07-003` | item não descartado, propagação multi-dia, reprocessamento exato e ciclo do alerta |
| Multi-tenant | `@SCN-RNF06-001..007`, com anti-enumeração em `@SCN-RF02-006` | tokens distintos, mesma chave permitida entre comerciantes e ausência de influência cruzada |
| Retroatividade e fuso | `@SCN-RF01-002..005`, `@SCN-RF04-005`, `@SCN-RF04A-003`, `@SCN-RNF06-006` | relógio controlado, D-30/D-31, snapshots multi-dia e histórico não reclassificado |
| Carga 50 RPS | `@SCN-RNF03-001`, com estado correto apoiado por `@SCN-RF05-*` | k6 em taxa aberta, massa versionada, 15.000 como denominador e check de saldo por resposta |

## Critério para avançar uma tag

Antes de adicionar uma `@SCN-*` ao manifesto, registrar na revisão:

- binding e fixture;
- nível de teste usado;
- dependências reais ou substitutos justificados;
- comando reproduzível;
- oráculo e condição de falha;
- caminho do relatório esperado.

Depois da execução, o relatório deve listar a tag. A ausência da tag na evidência mantém o cenário apenas como implementado ou pendente, nunca verificado.

## Evidência existente versus faltante

| Evidência | Estado atual |
|---|---|
| Catálogo de 14 features/81 tags | Gerado por `make reports`/CI em `evidence/reports/bdd-catalog.json`; ignorado no clone |
| Saída JSON dos testes Go | Gerada por `make reports`/CI em `evidence/reports/go-test.json`; ignorada no clone |
| Resultado comportamental Godog por SCN | Ausente |
| Bindings reais listados no manifesto | Ausente; manifesto vazio |
| Integração/Testcontainers | Ausente |
| Falha, outbox, DLQ e reconciliação | Ausente |
| Testes multi-tenant executados | Ausente |
| Script e relatório k6 | Ausente |
| Traces, métricas e alertas de aceite | Ausente |
