# DevSecOps delivery foundation

GitHub Actions orchestrates the same commands developers run locally. No workflow in this phase has package, attestation, OIDC, registry, environment, publication, or deployment permission.

## Local gates

| Command | Real proof in this snapshot | Evidence |
|---|---|---|
| `make ci` | generated-code drift, formatting, Buf lint/breaking, build, vet, race tests and BDD catalog | `evidence/reports/ci/` via `make reports` |
| `make policy` | actionlint, full-SHA Action pins, least privilege, hosted runners, timeouts/concurrency and negative unpinned-Action fixture | `evidence/reports/policy/` |
| `make security` | govulncheck, Gitleaks history/worktree, Trivy filesystem/config and controlled negative secret/vulnerability fixtures | `evidence/reports/security/` |
| `make build-validation` | two identical Go builds, SHA-256 checksums, CycloneDX/SPDX SBOM | `evidence/reports/build/` |
| `make full-validation` | all materialized gates above; continues after a failed gate so all possible evidence exists | `evidence/reports/full/` plus gate directories |

Tool binaries live under ignored `.tools/bin`. Go-installed tools use version sentinels; downloaded Gitleaks and Trivy releases are fixed by version, platform, and SHA-256. A clean clone needs Go 1.26.5 plus standard `curl`, `tar`, and `shasum`, but no preinstalled security scanner.

Gitleaks scans only the current branch ancestry (`HEAD`) so unrelated local worktree refs cannot contaminate evidence. `.gitleaksignore` contains one immutable fingerprint for the inert token committed by `e674d01`; it cannot suppress another rule, line, path, or commit. Negative fixtures are generated only in a temporary directory and prove that an additional default-rule finding remains blocking.

Govulncheck text mode is the blocking execution; JSON runs separately as evidence because JSON mode can return zero while reporting reachable traces. Its negative fixture builds a temporary module with a known reachable call and invokes the same production wrapper. Trivy always emits JSON with exit zero, then `cmd/securitypolicy` applies the blocking severity/fix policy; the synthetic Trivy fixture exercises that exact parser.

There is no `integration` target yet: API contract tests and the Godog catalog are already part of CI and do not prove an external boundary. The first datastore, broker, or container producer must materialize integration testing and only then add it to full validation.

There is no Containerfile or image producer yet. Build validation records image scanning as not applicable instead of creating an empty job. The ticket that introduces the first image must add build and Trivy image scanning together.

## Workflows and stable checks

| Workflow | Stable check | Trigger |
|---|---|---|
| `ci.yml` | `foundation` | PR and `main` |
| `policy.yml` | `workflow-policy` | PR and `main` |
| `security.yml` | `source-security` | PR, `main`, weekly, manual |
| `security.yml` | `dependency-review` | PR when public or `DEPENDENCY_REVIEW_ENABLED=true` |
| `security.yml` | `security-sarif-upload` | when public or `CODE_SCANNING_ENABLED=true` |
| `codeql.yml` | `codeql-go-kotlin`, `codeql-actions` | when public or `CODE_SCANNING_ENABLED=true` |
| `build-validation.yml` | `validate-build` | `main`, version tags, manual |
| `full-validation.yml` | `validate-all` | `main`, manual |

Every external Action reference is an immutable 40-character commit SHA with the release version in a comment. Workflows start with `contents: read`; only SARIF/CodeQL jobs add `security-events: write`. PR code never uses `pull_request_target`, secrets, or self-hosted runners.

Evidence upload steps use `if: always()` and do not suppress gate exit codes. Reports include the commit, UTC execution time, result, and scanner versions. Retention is 14 days for CI/policy and 30 days for security/build/full validation.

## GitHub capability and governance status

The official repository remains private. On 2026-07-31, querying `repos/higordiego/keyrus-test-arch/branches/main/protection` returned HTTP 403 because branch protection for this private repository requires GitHub Pro or a change to public visibility. The chosen decision is to keep it private and defer protection; protection is **not active**.

Code scanning/CodeQL, Dependency Review enforcement, secret scanning, and push protection are also conditional on repository visibility and the GitHub plan/security products available to the owner. The workflows are implemented, but licensed jobs stay disabled on the private repository unless the corresponding repository variable is explicitly set after capability verification:

- `CODE_SCANNING_ENABLED=true` only after code scanning is available and enabled;
- `DEPENDENCY_REVIEW_ENABLED=true` only after Dependency Review is available;
- enable secret scanning and push protection in repository settings when offered.

Do not make those variables true merely to bypass the guard: an unavailable GitHub feature will make the real job fail.

## Applying `main` protection later

After the repository is public or the owner has a plan supporting private branch protection:

1. Push these workflows and open a PR so every intended required check has reported at least once.
2. Verify CodeQL/Dependency Review capability; enable their variables and run them if they will be required.
3. Run `scripts/apply-main-protection.sh` to inspect current state.
4. Run `scripts/apply-main-protection.sh --apply` to require all core and licensed checks, or `--apply --core-only` if licensed security features remain unavailable.
5. Re-run the read-only command and verify PR requirement, one approval, resolved conversations, strict/up-to-date checks, and the expected contexts.

The script is idempotent and targets only `higordiego/keyrus-test-arch:main`. It does not change visibility, secrets, Actions policy, or deployment settings.

## Dependabot and exceptions

Dependabot checks Go modules, GitHub Actions, and Docker inputs weekly. It does not auto-merge. License blocking remains deferred until an allow/deny policy is approved; Dependency Review still blocks new HIGH/CRITICAL vulnerabilities when available.

Any temporary vulnerability or policy exception needs an owner, justification, mitigation, expiry, and removal criteria. See [security-exceptions.md](security-exceptions.md).
