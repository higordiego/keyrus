# syntax=docker/dockerfile:1
# Builds both migration binaries into one small image. Each Compose/Swarm
# service that uses this image selects which one to run via `command:`, so
# there is exactly one Containerfile to keep pinned/patched instead of two
# near-identical ones.
# golang:1.26.5-alpine
FROM golang@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY services ./services
COPY internal ./internal
COPY gen ./gen
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ledger-migrate ./services/ledger/cmd/migrate \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/consolidation-migrate ./services/consolidation/cmd/migrate

# alpine:3.22
FROM alpine@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce
RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 65532 cashflow \
    && adduser -S -D -H -u 65532 -G cashflow cashflow
COPY --from=build /out/ledger-migrate /usr/local/bin/ledger-migrate
COPY --from=build /out/consolidation-migrate /usr/local/bin/consolidation-migrate
USER 65532:65532
# No ENTRYPOINT/default CMD: the Compose/Swarm service picks the binary to
# run via `command:`, so a misconfigured service fails closed (no command)
# instead of silently running the wrong migration.
