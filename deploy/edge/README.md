# Public edge

`krakend/krakend.json` is the allow-listed public surface for KrakenD Community
Edition 2.10. It publishes five business routes and the minimum OIDC protocol
surface. It does not publish Keycloak administration, health, metrics, the
master realm, application management endpoints, or internal gRPC methods.

Protected routes pin RS256, issuer, audience and exact scopes. Headers are
allow-listed: `Authorization`, W3C trace context and, on command routes,
`Idempotency-Key` reach the adapter; spoofable tenant/proxy headers and
`baggage` do not.

Public `tracestate` is untrusted. The adapters preserve only the fixed,
data-free marker `cashflow=public-edge`; every other value is dropped. The
Collector also clears span `trace_state` before export, so KrakenD's earlier
instrumentation cannot export an opaque client value. `traceparent` remains
the correlation source across HTTP and gRPC.

No retry component is configured on either business `POST`. A failed response
is returned to the client, whose explicit repetition must reuse the same
`Idempotency-Key`. The router rate limits are local to each KrakenD replica:
they are abuse protection, not a distributed quota or a financial control.

The three browser/token OIDC routes have a 15-second backend budget. The global
`write_timeout` is 20 seconds so a cold Keycloak still has five seconds of
response-write margin; it must always remain greater than the largest endpoint
timeout. Transport errors are terminal in the PKCE smoke and are never retried.

The final image declares a Docker `HEALTHCHECK` against KrakenD's built-in
loopback `GET /__health` probe. The E2E waits for Docker state `healthy`, asserts
UID/GID 65532 and separately proves that application/Keycloak management,
health and metrics paths are absent from the public route allowlist.

Validate the file with the same pinned Community Edition line:

```sh
docker run --rm \
  -v "$PWD/deploy/edge/krakend/krakend.json:/etc/krakend/krakend.json:ro" \
  krakend:2.10 check -c /etc/krakend/krakend.json --lint
```

The Go validator in `internal/platform/edge/krakendconfig` adds project-specific
invariants that the upstream schema cannot express.
