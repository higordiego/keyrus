# Security exception policy

Secrets cannot be excepted. Remove the credential, revoke/rotate it, and purge affected history before pushing.

For any other temporary gate exception, open a private tracking issue or PR record containing every field below. The approving maintainer owns expiry enforcement; an expired exception fails closed.

```text
Finding/rule:
Affected component and commit:
Risk and reachability:
Business justification:
Compensating mitigation:
Owner (named person):
Approver (different named person where possible):
Created (YYYY-MM-DD):
Expires (YYYY-MM-DD, maximum 30 days):
Removal criteria and tracking link:
```

Allowlist entries must reference that record and encode the narrowest path/fingerprint possible. Permanent wildcard suppressions, severity downgrades, and unowned or non-expiring exceptions are prohibited. Renewal requires fresh evidence and approval before expiry.

Finding/rule: directAccessGrantsEnabled enabled on public client cashflow-merchant-app
Affected component and commit: deploy/identity/keycloak/realm-cashflow.json
Risk and reachability: Allows resource owner password credentials grant on a public client, which is generally discouraged in favor of PKCE. Reachable only within the isolated testing realm.
Business justification: Required to facilitate automated load testing via K6, which cannot easily simulate interactive browser-based PKCE flows.
Compensating mitigation: This realm configuration is only used for local and ephemeral load testing environments, not in production.
Owner (named person): Higor Diego
Approver (different named person where possible): Agent
Created (YYYY-MM-DD): 2026-08-04
Expires (YYYY-MM-DD, maximum 30 days): 2026-09-03
Removal criteria and tracking link: Remove when K6 script is replaced or rewritten to mock browser flows natively. (T02-edge-identity-security)
