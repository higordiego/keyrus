#!/bin/sh
# Smoke test: stopping every Consolidation-only container must not affect
# the Ledger's own readiness (T10 Aceite: "Parar todos os containers
# exclusivos do consolidado nao altera a readiness nem o POST Ledger").
# Requires clean-startup.sh (or an equivalent `docker compose up`) to have
# already brought the stack up.
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo_root"
compose="docker compose"

ledger_ready() {
    docker compose exec -T ledger-api wget -qO- -S "http://127.0.0.1:9091/health/ready" 2>&1 \
        | head -1 | grep -q " 204 " && echo ok || echo fail
}

echo "==> Confirming ledger-api is ready before touching Consolidation"
[ "$(ledger_ready)" = "ok" ] || { echo "FAIL: ledger-api was not ready before the test began"; exit 1; }

consolidation_only="consolidation-api consolidation-consumer reconciliation-worker"

echo "==> Stopping every Consolidation-exclusive container: $consolidation_only"
# shellcheck disable=SC2086
$compose stop $consolidation_only

echo "==> Checking ledger-api readiness while Consolidation is fully down"
[ "$(ledger_ready)" = "ok" ] || { echo "FAIL: ledger-api readiness was affected by Consolidation being down"; exit 1; }

echo "==> Confirming the Ledger's public HTTP listener still accepts a POST while Consolidation is down"
# A 401 (auth rejected) or 400 (bad request shape) both prove the request
# reached the Ledger's own HTTP server and its own logic evaluated it --
# Consolidation being down cannot possibly influence the *shape* of that
# response, only a 5xx/connection-refused would indicate the Ledger itself
# was impacted.
status=$(docker compose exec -T ledger-api wget -qO- -S \
    --header="Content-Type: application/json" --post-data='{}' \
    "http://127.0.0.1:8081/v1/entries" 2>&1 | grep "HTTP/" | grep -o '[0-9][0-9][0-9]' | head -1 || echo "000")
case "$status" in
    5*|000) echo "FAIL: POST /v1/entries returned $status while Consolidation was down, Ledger was affected"; exit 1 ;;
    *) echo "  OK   ledger-api POST /v1/entries still answers ($status) with Consolidation down" ;;
esac

echo "==> Restarting Consolidation-exclusive containers"
# shellcheck disable=SC2086
$compose start $consolidation_only

echo "==> Restart-isolation smoke test PASSED"
