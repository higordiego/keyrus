#!/bin/sh
# Same evidence-key handling as run-tests.sh, writing JSON test output instead
# of failing fast: the key lives only in a private (0600) temporary file, and
# only its path crosses into the two `go test` invocations below.
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

mkdir -p evidence/reports
evidence_file="$repo_root/.tools/runtime-evidence.json"
rm -f "$evidence_file"

umask 077
evidence_key_file=$(mktemp)
trap 'rm -f "$evidence_key_file"' EXIT HUP INT TERM
head -c32 /dev/urandom | od -An -tx1 | tr -d ' \n' >"$evidence_key_file"

export CASHFLOW_RUNTIME_EVIDENCE_FILE="$evidence_file"
export CASHFLOW_RUNTIME_EVIDENCE_KEY_FILE="$evidence_key_file"

go test -race -count=1 -timeout 45m -json -run '^TestRealEdgeIdentityRuntime$' ./test/integration >evidence/reports/go-test.json
CASHFLOW_SKIP_REAL_E2E=1 go test -race -count=1 -timeout 45m -json ./... >>evidence/reports/go-test.json
