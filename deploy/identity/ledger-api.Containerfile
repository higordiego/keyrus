# syntax=docker/dockerfile:1
# golang:1.26.5-alpine
FROM golang@sha256:0d1d3a794be25f809dd2cb3160d8c73276c4056a9f8242a138e908ddeee7b6b6 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY gen ./gen
COPY services ./services
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ledger-api ./services/ledger/cmd/ledger-api

# alpine:3.22
FROM alpine@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce
RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 65532 cashflow \
    && adduser -S -D -H -u 65532 -G cashflow cashflow
COPY --from=build /out/ledger-api /usr/local/bin/ledger-api
USER 65532:65532
EXPOSE 8081 9081 9091
HEALTHCHECK --interval=10s --timeout=2s --retries=3 CMD wget -q -O /dev/null http://127.0.0.1:9091/health/ready || exit 1
ENTRYPOINT ["/usr/local/bin/ledger-api"]
