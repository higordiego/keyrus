# Comandos

Binários utilitários usados pelos gates locais/CI, não pelos serviços de negócio (esses moram em `services/*/cmd/`):

- `bddcheck/`: valida o catálogo Gherkin contra o manifesto (`make bdd-parse`).
- `securitypolicy/`: aplica a política bloqueante de severidade/fix sobre os relatórios do Trivy/govulncheck (`make security`, `make build-validation`).
- `workflowpolicy/`: valida pins de Action, privilégio mínimo e demais regras dos workflows do GitHub Actions (`make policy`).
