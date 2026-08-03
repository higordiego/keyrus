# syntax=docker/dockerfile:1
FROM golang:1.26.5-alpine AS build
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

FROM alpine:3.22
RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 65532 cashflow \
    && adduser -S -D -H -u 65532 -G cashflow cashflow
COPY --from=build /out/ledger-api /usr/local/bin/ledger-api
USER 65532:65532
EXPOSE 8081 9081 9091
HEALTHCHECK --interval=10s --timeout=2s --retries=3 CMD wget -q -O /dev/null http://127.0.0.1:9091/health/ready || exit 1
ENTRYPOINT ["/usr/local/bin/ledger-api"]
