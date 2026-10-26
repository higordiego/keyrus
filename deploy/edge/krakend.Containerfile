# golang:1.24.5-alpine3.21
FROM golang@sha256:6edc20586dd08dacad538c1f09984bc2aa61720be59056cf75429691f294d731 AS plugin-builder

RUN apk add --no-cache build-base binutils-gold
WORKDIR /src
COPY deploy/edge/krakend/plugins/no-redirect/ ./
RUN mkdir /out \
    && go test ./... \
    && go build -buildmode=plugin -o /out/cashflow-no-redirect.so .

# krakend:2.10.2
FROM krakend@sha256:8b1ddc8546ad71b4b43dbc977b7b55873d853754ef75eccc52a6177c281dd568

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
