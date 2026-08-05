#!/bin/sh
# Smoke test: losing one instance of a stateless service must not corrupt
# state or block recovery. This Compose stack runs exactly one instance of
# each service (no `replicas: N`, a Swarm-only concept, out of scope for
# this pass), so "replica loss" here means "the single instance dies and
# comes back", proving the service is stateless and safe to restart, the
# same property real replica loss depends on.
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo_root"
compose="docker compose"

for service in krakend ledger-api consolidation-api; do
    echo "==> Killing $service without warning (simulating an ungraceful replica loss)"
    $compose kill "$service"

    echo "==> Bringing it back up"
    $compose up -d "$service"

    echo "==> Waiting for $service to report healthy again"
    elapsed=0
    budget=60
    while :; do
        container=$($compose ps -q "$service")
        health=$(docker inspect "$container" --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' 2>/dev/null || echo "")
        case "$health" in
            healthy|running) break ;;
        esac
        elapsed=$((elapsed + 2))
        if [ "$elapsed" -ge "$budget" ]; then
            echo "FAIL: $service did not recover within ${budget}s after being killed"
            docker compose logs --tail=50 "$service"
            exit 1
        fi
        sleep 2
    done
    echo "  OK   $service recovered after ${elapsed}s"
done

echo "==> Confirming the rest of the stack is still healthy after all three restarts"
still_healthy=true
for service in postgres rabbitmq keycloak ledger-outbox-publisher consolidation-consumer reconciliation-worker; do
    container=$($compose ps -q "$service")
    health=$(docker inspect "$container" --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' 2>/dev/null || echo "")
    case "$health" in
        healthy|running) ;;
        *) echo "FAIL: $service is $health after the replica-loss test"; still_healthy=false ;;
    esac
done
[ "$still_healthy" = true ] || exit 1

echo "==> Replica-loss smoke test PASSED"
