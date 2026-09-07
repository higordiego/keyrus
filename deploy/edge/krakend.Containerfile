# golang:1.24.5-alpine3.21
FROM golang@sha256:512690a5660563b57d37ecc31129e7f136e831db2aed24a1dbeb8ad7380dc0fa AS plugin-builder

RUN apk add --no-cache build-base binutils-gold
WORKDIR /src
COPY deploy/edge/krakend/plugins/no-redirect/ ./
RUN mkdir /out \
    && go test ./... \
    && go build -buildmode=plugin -o /out/cashflow-no-redirect.so .

# krakend:2.10.2
FROM krakend@sha256:639e285e3f6621a3a171c5c5bdf4fc3e2b5b8d016a0bf9c2da6f748e1973e020

COPY --from=plugin-builder /out/cashflow-no-redirect.so /etc/krakend/plugins/cashflow-no-redirect.so
COPY deploy/edge/krakend/krakend.json /etc/krakend/krakend.json

USER 65532:65532
# The built-in /__health endpoint bypasses the router's host allowlist and TLS
# redirect, so it is disabled in krakend.json (router.disable_health) and would
# otherwise be reachable, unauthenticated, on the very port published to
# clients. This probe instead confirms the gateway's own HTTP server answers on
# its listening port; any status line counts, including the 404 every
# undeclared path already returns, without adding a route of its own.
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=6 \
    CMD ["/bin/sh", "-c", "wget -q -S -O /dev/null http://127.0.0.1:8080/ 2>&1 | grep -q 'HTTP/'"]
ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["krakend", "run", "-c", "/etc/krakend/krakend.json"]
