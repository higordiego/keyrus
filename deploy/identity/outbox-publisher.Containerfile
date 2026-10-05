# syntax=docker/dockerfile:1
# golang:1.26.5-alpine
FROM golang@sha256:e0174e51e81218523251d85d248a90d24c3d5e81543b4f07a5d66229397db190 AS build
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
FROM alpine@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce
RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 65532 cashflow \
    && adduser -S -D -H -u 65532 -G cashflow cashflow
COPY --from=build /out/outbox-publisher /usr/local/bin/outbox-publisher
COPY --from=build /out/outbox-topology /usr/local/bin/outbox-topology
USER 65532:65532
# No default ENTRYPOINT: the Compose service picks the binary via `command:`.
