# Deploy

Artefatos de container e orquestração usados pelo `docker-compose.yaml` da raiz:

- `edge/`: KrakenD (gateway) e seu Containerfile.
- `identity/`: Containerfiles de cada serviço (Ledger API, Consolidation API, Consolidation Consumer, Outbox Publisher, Reconciliation Worker, migrate) e o Keycloak com o realm de teste (`identity/keycloak/`).
- `observability/`: configuração do Prometheus (scrape configs e regras de alerta) e provisionamento do Grafana (datasource e dashboard).
- `rabbitmq/`: plugins habilitados (management + Prometheus exporter).

Todas as imagens do `docker-compose.yaml` são fixadas por digest SHA-256, não por tag. Veja o [README raiz](../README.md#como-rodar-localmente) para como subir a stack.
