# Cashflow realm

`realm-cashflow.json` is a credential-free Keycloak import template. It defines:

- the public merchant application using Authorization Code with PKCE;
- separate public and internal audiences;
- least-privilege service identities for consolidation and reconciliation;
- exact merchant, ledger, consolidation and operational scopes;
- two local merchant fixtures whose `merchant_id` is emitted by a mapper.

Render it outside the repository by supplying the four required secrets through
the environment or Docker secrets under `/run/secrets`:

```sh
deploy/identity/keycloak/render-realm.sh /secure/runtime/realm-cashflow.json
```

The renderer refuses empty or unresolved credentials and creates the result
with mode `0600`. The generated file contains credentials and must never be
committed or logged.

Production runtime configuration belongs to the deployment topology ticket. It
must run Keycloak in optimized mode with an external PostgreSQL identity
schema, at least two replicas, `jdbc-ping`, serial rollout, TLS-aware proxy
settings, and health/metrics restricted to the management network. Only the
OIDC paths enumerated in the KrakenD configuration are public.

`go test ./test/integration -run TestKeycloakRealmWithRealIssuer -count=1 -v`
imports the rendered realm into the pinned Keycloak image using Testcontainers
and verifies real client-credential tokens through the production JWKS verifier.
