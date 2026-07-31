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
