#!/bin/sh
set -eu

actionlint=$1
make policy-tools
"$actionlint" .github/workflows/*.yml
./scripts/check-workflow-policy.sh .github/workflows

tmp_log=$(mktemp "${TMPDIR:-/tmp}/keyrus-policy-negative.XXXXXX")
trap 'rm -f "$tmp_log"' EXIT HUP INT TERM
if ./scripts/check-workflow-policy.sh test/fixtures/security/unpinned-action.yml >"$tmp_log" 2>&1; then
	echo "negative workflow fixture unexpectedly passed" >&2
	exit 1
fi
if ! grep -q 'full 40-character commit SHA' "$tmp_log"; then
	echo "negative workflow fixture failed for an unexpected reason" >&2
	cat "$tmp_log" >&2
	exit 1
fi
echo "negative workflow fixture rejected for missing immutable SHA"
