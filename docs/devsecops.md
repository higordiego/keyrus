# Base de entrega DevSecOps

O GitHub Actions orquestra os mesmos comandos que os desenvolvedores executam localmente. Nenhum workflow nesta fase possui permissões de package, attestation, OIDC, registry, environment, publication, ou deployment.

## Gates locais

| Command | Prova real neste snapshot | Evidence (Evidência) |
|---|---|---|
| `make ci` | generated-code drift, formatação, Buf lint/breaking, build, vet, race tests e catálogo BDD | `evidence/reports/ci/` via `make reports` |
| `make policy` | actionlint, full-SHA Action pins, least privilege, hosted runners, timeouts/concurrency e fixture negativa de unpinned-Action | `evidence/reports/policy/` |
| `make security` | govulncheck, histórico/worktree do Gitleaks, filesystem/config do Trivy e fixtures controladas negativas de secret/vulnerability | `evidence/reports/security/` |
| `make build-validation` | dois Go builds idênticos, checksums SHA-256, SBOM CycloneDX/SPDX | `evidence/reports/build/` |
| `make full-validation` | todos os gates materializados acima; continua após uma falha de gate para que todas as evidências possíveis existam | `evidence/reports/full/` mais diretórios de gates |

Os binários das ferramentas residem no `.tools/bin` que é ignorado. As ferramentas instaladas via Go usam version sentinels; os releases baixados do Gitleaks e do Trivy são fixados por versão, plataforma e SHA-256. Um clone limpo precisa do Go 1.26.5 além de `curl`, `tar` e `shasum` padrão, mas sem scanner de segurança pré-instalado.

O Gitleaks verifica apenas a ancestralidade da branch atual (`HEAD`) para que referências de worktree locais não relacionadas não contaminem as evidências. O `.gitleaksignore` contém um fingerprint imutável para o token inerte comitado por `e674d01`; ele não pode suprimir outra regra, linha, path ou commit. Fixtures negativas são geradas apenas em um diretório temporário e provam que uma descoberta de regra padrão (default-rule) adicional permanece sendo bloqueante.

O modo de texto do govulncheck é a execução bloqueante; JSON é executado separadamente como evidência porque o modo JSON pode retornar zero enquanto reporta traços alcançáveis (reachable traces). Seu fixture negativo faz o build de um módulo temporário com uma chamada alcançável (reachable call) conhecida e invoca o mesmo wrapper de produção. O Trivy sempre emite JSON com exit zero, então o `cmd/securitypolicy` aplica a política bloqueante de severity/fix; o fixture sintético do Trivy exercita aquele exato parser.

Ainda não há um alvo de `integration`: testes de contrato de API e o catálogo Godog já fazem parte da CI e não provam um boundary (limite) externo. O primeiro produtor de datastore, broker ou contêiner deve materializar testes de integração e só então adicioná-lo à full validation (validação completa).

Ainda não há um Containerfile ou produtor de imagens. A build validation (validação de build) registra o scan de imagens como não aplicável em vez de criar um job vazio. O ticket que introduz a primeira imagem deve adicionar o build e o Trivy image scanning juntos.

## Workflows e stable checks (verificações estáveis)

| Workflow | Stable check | Trigger (Gatilho) |
|---|---|---|
| `ci.yml` | `foundation` | PR e `main` |
| `policy.yml` | `workflow-policy` | PR e `main` |
| `security.yml` | `source-security` | PR, `main`, weekly, manual |
| `security.yml` | `dependency-review` | PR quando público ou `DEPENDENCY_REVIEW_ENABLED=true` |
| `security.yml` | `security-sarif-upload` | quando público ou `CODE_SCANNING_ENABLED=true` |
| `codeql.yml` | `codeql-go-kotlin`, `codeql-actions` | quando público ou `CODE_SCANNING_ENABLED=true` |
| `build-validation.yml` | `validate-build` | `main`, version tags, manual |
| `full-validation.yml` | `validate-all` | `main`, manual |

Cada referência de Action externa é um commit SHA imutável de 40 caracteres com a versão de lançamento em um comentário. Workflows começam com `contents: read`; apenas os jobs de SARIF/CodeQL adicionam `security-events: write`. Códigos de PR nunca usam `pull_request_target`, secrets, ou self-hosted runners.

Passos de upload de evidências usam `if: always()` e não suprimem os códigos de saída dos gates. Os relatórios incluem o commit, tempo de execução UTC, resultado e versões do scanner. A retenção é de 14 dias para CI/policy e de 30 dias para security/build/full validation.

## Capability do GitHub e status de governança

O repositório oficial permanece privado. Em 31-07-2026, a consulta a `repos/higordiego/keyrus-test-arch/branches/main/protection` retornou HTTP 403 porque a proteção de branch para este repositório privado requer GitHub Pro ou uma mudança para visibilidade pública. A decisão escolhida é mantê-lo privado e adiar a proteção; a proteção **não está ativa**.

Code scanning/CodeQL, imposição de Dependency Review, secret scanning, e push protection também são condicionais à visibilidade do repositório e ao plano do GitHub/produtos de segurança disponíveis ao proprietário. Os workflows estão implementados, mas os jobs licenciados permanecem desativados no repositório privado, a menos que a variável correspondente do repositório seja explicitamente configurada após a verificação de capability (capacidade):

- `CODE_SCANNING_ENABLED=true` somente depois que o code scanning estiver disponível e ativado;
- `DEPENDENCY_REVIEW_ENABLED=true` somente depois que o Dependency Review estiver disponível;
- ative secret scanning e push protection nas configurações do repositório quando oferecido.

Não torne essas variáveis verdadeiras apenas para contornar a barreira: uma feature não disponível do GitHub fará o job real falhar.

## Aplicando proteção na `main` depois

Depois que o repositório se tornar público ou o proprietário possuir um plano que suporte a proteção de branches privadas:

1. Faça push desses workflows e abra um PR para que cada verificação requerida tenha sido reportada pelo menos uma vez.
2. Verifique a capability do CodeQL/Dependency Review; ative suas variáveis e execute-as se forem ser requeridas.
3. Execute `scripts/apply-main-protection.sh` para inspecionar o estado atual.
4. Execute `scripts/apply-main-protection.sh --apply` para exigir todos os core e licensed checks, ou `--apply --core-only` se os recursos licenciados de segurança continuarem indisponíveis.
5. Execute novamente o comando em modo somente leitura (read-only) e verifique o requerimento do PR, uma aprovação, conversas resolvidas, strict/up-to-date checks e os contextos esperados.

O script é idempotente e foca apenas em `higordiego/keyrus-test-arch:main`. Ele não muda a visibilidade, secrets, Actions policy ou configurações de deployment.

## Dependabot e exceções

O Dependabot verifica módulos Go, GitHub Actions e inputs do Docker semanalmente. Ele não faz auto-merge. O bloqueio por licença permanece adiado até que uma política de allow/deny (permitir/negar) seja aprovada; O Dependency Review ainda bloqueia novas vulnerabilidades de nível HIGH/CRITICAL quando disponíveis.

Qualquer vulnerabilidade temporária ou exceção de política precisa de um owner (proprietário), justificativa, mitigação, validade, e critérios de remoção. Consulte [security-exceptions.md](security-exceptions.md).
