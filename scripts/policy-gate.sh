#!/bin/sh
set -eu

actionlint=$1
make policy-tools
"$actionlint" .github/workflows/*.yml
./scripts/check-workflow-policy.sh .github/workflows

tmp_log=$(mktemp "${TMPDIR:-/tmp}/keyrus-policy-negative.XXXXXX")
trap 'rm -f "$tmp_log"' EXIT HUP INT TERM

assert_rejected() {
	fixture=$1
	expected=$2
	if ./scripts/check-workflow-policy.sh "$fixture" >"$tmp_log" 2>&1; then
		echo "$fixture unexpectedly passed workflow policy" >&2
		exit 1
	fi
	if ! grep -q "$expected" "$tmp_log"; then
		echo "$fixture failed for an unexpected reason" >&2
		cat "$tmp_log" >&2
		exit 1
	fi
	echo "$fixture rejected: $expected"
}

assert_rejected test/fixtures/workflow-policy/unpinned-action.yml 'full immutable digest/SHA'
assert_rejected test/fixtures/workflow-policy/quoted-uses.yml 'full immutable digest/SHA'
assert_rejected test/fixtures/workflow-policy/permissions-write-all.yml 'top-level permissions must be an explicit mapping'
assert_rejected test/fixtures/workflow-policy/unauthorized-write.yml 'permissions must be exactly contents:read'
assert_rejected test/fixtures/workflow-policy/self-hosted-indirect.yml 'runs-on must be an explicit GitHub-hosted Ubuntu label'
assert_rejected test/fixtures/workflow-policy/secret-access.yml 'secret context access is forbidden'
