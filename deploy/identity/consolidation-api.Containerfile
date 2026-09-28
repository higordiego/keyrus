# syntax=docker/dockerfile:1
# golang:1.26.5-alpine
FROM golang@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY services ./services
COPY internal ./internal
COPY gen ./gen
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/consolidation-api ./services/consolidation/cmd/consolidation-api

# alpine:3.22
FROM alpine@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 65532 cashflow \
    && adduser -S -D -H -u 65532 -G cashflow cashflow
COPY --from=build /out/consolidation-api /usr/local/bin/consolidation-api
USER 65532:65532
EXPOSE 8082 9092
HEALTHCHECK --interval=10s --timeout=2s --retries=3 CMD wget -q -O /dev/null http://127.0.0.1:9092/health/ready || exit 1
ENTRYPOINT ["/usr/local/bin/consolidation-api"]
