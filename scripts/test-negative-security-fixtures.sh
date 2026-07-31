#!/bin/sh
set -eu

gitleaks=$1
tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/keyrus-security-fixtures.XXXXXX")
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM

cp test/fixtures/security/negative-secret.txt "$tmp_dir/unsafe-secret.txt"
if "$gitleaks" dir --no-banner --config .gitleaks.toml --report-format json --report-path "$tmp_dir/gitleaks.json" "$tmp_dir" >"$tmp_dir/gitleaks.log" 2>&1; then
	echo "controlled secret fixture unexpectedly passed" >&2
	exit 1
fi
if ! grep -q 'devsecops-fixture-token' "$tmp_dir/gitleaks.json"; then
	echo "controlled secret fixture failed without the expected rule" >&2
	cat "$tmp_dir/gitleaks.log" >&2
	exit 1
fi
echo "controlled inert secret fixture rejected by devsecops-fixture-token"

if go run ./cmd/securitypolicy -trivy-report test/fixtures/security/blocking-vulnerability.json >"$tmp_dir/vulnerability.log" 2>&1; then
	echo "controlled vulnerability fixture unexpectedly passed" >&2
	exit 1
fi
if ! grep -q 'blocking vulnerability CVE-2099-0001 severity CRITICAL' "$tmp_dir/vulnerability.log"; then
	echo "controlled vulnerability fixture failed for an unexpected reason" >&2
	cat "$tmp_dir/vulnerability.log" >&2
	exit 1
fi
echo "controlled vulnerability fixture rejected by HIGH/CRITICAL-with-fix policy"
