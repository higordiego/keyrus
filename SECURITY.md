# Security policy

## Reporting a vulnerability

Do not open a public issue with exploit details, credentials, customer data, or financial data. Use GitHub's private vulnerability reporting when it is enabled for this repository. Until then, contact the repository owner through a private channel and include the affected commit, impact, reproduction steps, and a safe proof of concept.

The maintainer will acknowledge a report within two business days, triage severity and reachability, and coordinate remediation and disclosure. There is no bug-bounty promise.

## Supported versions

Only the latest commit on `main` is supported during the architecture challenge. Secrets are never accepted in reports, fixtures, logs, or commits.

## Security gates

`make security` scans reachable Go vulnerabilities, repository history, the working tree, dependencies, and configuration. `make policy` enforces immutable Action references and least-privilege workflow policy. HIGH/CRITICAL reachable vulnerabilities, confirmed secrets, and critical insecure configuration block changes.

Exceptions must follow [docs/security-exceptions.md](docs/security-exceptions.md); secret findings cannot be waived.
