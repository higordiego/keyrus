#!/bin/sh
# Smoke test: `docker compose up --build` on a machine with no prior state
# (no volumes, no cached migration status) reaches full health with no
# manual step. Fails closed: any container that isn't healthy/running -- or
# a migration container that didn't exit 0 -- fails the script.
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo_root"

budget_seconds=${SMOKE_STARTUP_BUDGET_SECONDS:-180}
compose="docker compose"

echo "==> Tearing down any existing stack and volumes (clean-machine simulation)"
$compose down -v --remove-orphans >/dev/null 2>&1 || true

echo "==> docker compose up -d --build (app profile, via .env's COMPOSE_PROFILES)"
$compose up -d --build

expected_running="postgres rabbitmq keycloak otel-collector ledger-api consolidation-api ledger-outbox-publisher consolidation-consumer reconciliation-worker krakend"
expected_completed="ledger-migrate consolidation-migrate"

elapsed=0
while :; do
    all_ready=true

    for service in $expected_completed; do
        container=$(docker compose ps -a -q "$service" 2>/dev/null || true)
        [ -z "$container" ] && { all_ready=false; continue; }
        state=$(docker inspect "$container" --format '{{.State.Status}}' 2>/dev/null || echo "")
        if [ "$state" != "exited" ]; then
            all_ready=false
            continue
        fi
        exit_code=$(docker inspect "$container" --format '{{.State.ExitCode}}' 2>/dev/null || echo 1)
        if [ "$exit_code" != "0" ]; then
            echo "FAIL: $service exited with code $exit_code (migration failed)"
            docker compose logs "$service"
            exit 1
        fi
    done

    for service in $expected_running; do
        container=$(docker compose ps -q "$service" 2>/dev/null || true)
        if [ -z "$container" ]; then
            all_ready=false
            continue
        fi
        health=$(docker inspect "$container" --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' 2>/dev/null || echo "")
        case "$health" in
            healthy|running) ;;
            *) all_ready=false ;;
        esac
    done

    if [ "$all_ready" = true ]; then
        echo "==> All services healthy/running, migrations completed successfully, after ${elapsed}s"
        break
    fi

    if [ "$elapsed" -ge "$budget_seconds" ]; then
        echo "FAIL: stack did not reach full health within ${budget_seconds}s"
        docker compose ps
        exit 1
    fi

    sleep 3
    elapsed=$((elapsed + 3))
done

echo "==> Verifying health/readiness endpoints directly (not just container status)"
for check in \
    "ledger-api:9091:/health/ready" \
    "consolidation-api:9092:/health/ready" \
    "consolidation-consumer:8081:/readyz" \
    "reconciliation-worker:9092:/health/ready"; do
    service=$(echo "$check" | cut -d: -f1)
    port=$(echo "$check" | cut -d: -f2)
    path=$(echo "$check" | cut -d: -f3)
    status=$(docker compose exec -T "$service" wget -qO- -S "http://127.0.0.1:${port}${path}" 2>&1 | head -1 | grep -o '[0-9][0-9][0-9]' | head -1 || echo "000")
    case "$status" in
        204|200) echo "  OK   $service$path -> $status" ;;
        *) echo "FAIL: $service$path returned $status"; exit 1 ;;
    esac
done

echo "==> Clean startup smoke test PASSED"
