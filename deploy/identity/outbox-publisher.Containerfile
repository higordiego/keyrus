# syntax=docker/dockerfile:1
# golang:1.26.5-alpine
FROM golang@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY gen ./gen
COPY api ./api
COPY services ./services
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/outbox-publisher ./services/ledger/cmd/outbox-publisher \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/outbox-topology ./services/ledger/cmd/outbox-topology

# alpine:3.22
FROM alpine@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 65532 cashflow \
    && adduser -S -D -H -u 65532 -G cashflow cashflow
COPY --from=build /out/outbox-publisher /usr/local/bin/outbox-publisher
COPY --from=build /out/outbox-topology /usr/local/bin/outbox-topology
USER 65532:65532
# No default ENTRYPOINT: the Compose service picks the binary via `command:`.
