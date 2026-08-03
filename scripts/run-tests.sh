#!/bin/sh
# Runs the real T02 E2E once, then the rest of the suite, sharing one
# ephemeral evidence attestation key between both steps so the BDD suite
# reuses the evidence the E2E just produced instead of starting a second real
# stack.
#
# The key is minted straight into a private (0600) temporary file; only its
# path ever crosses a process boundary as an environment value, never the key
# bytes themselves. That keeps the key out of any build log, shell trace, or
# `make -n` dry-run output -- unlike a Makefile variable substituted into a
# recipe line, which `make` echoes by default even during a dry run.
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

evidence_file="$repo_root/.tools/runtime-evidence.json"
rm -f "$evidence_file"

umask 077
evidence_key_file=$(mktemp)
trap 'rm -f "$evidence_key_file"' EXIT HUP INT TERM
head -c32 /dev/urandom | od -An -tx1 | tr -d ' \n' >"$evidence_key_file"

export CASHFLOW_RUNTIME_EVIDENCE_FILE="$evidence_file"
export CASHFLOW_RUNTIME_EVIDENCE_KEY_FILE="$evidence_key_file"

go test -race -count=1 -timeout 45m -run '^TestRealEdgeIdentityRuntime$' ./test/integration
CASHFLOW_SKIP_REAL_E2E=1 go test -race -count=1 -timeout 45m ./...
(cd deploy/edge/krakend/plugins/no-redirect && go test -race -count=1 ./...)
