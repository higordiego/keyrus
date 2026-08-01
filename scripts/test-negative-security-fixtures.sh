#!/bin/sh
set -eu

gitleaks=$1
govulncheck=$2
tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/keyrus-security-fixtures.XXXXXX")
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM

{
	printf '%s%s%s\n' 'SECURITY_TEST_' 'TOKEN_0123456789ABCDEF' '0123456789ABCDEF'
	printf '%s%s%s\n' 'ghp_' '7A3b9C2d8E4f6G1h5J0k' '3L7m9N2p4Q6r8S1t'
} >"$tmp_dir/unsafe-secret.txt"
if "$gitleaks" dir --no-banner --config .gitleaks.toml --report-format json --report-path "$tmp_dir/gitleaks.json" "$tmp_dir" >"$tmp_dir/gitleaks.log" 2>&1; then
	echo "controlled secrets fixture unexpectedly passed" >&2
	exit 1
fi
if ! grep -q 'devsecops-fixture-token' "$tmp_dir/gitleaks.json"; then
	echo "controlled secrets fixture did not report the inert custom token" >&2
	cat "$tmp_dir/gitleaks.log" >&2
	exit 1
fi
if ! grep -q 'github-pat' "$tmp_dir/gitleaks.json"; then
	echo "controlled secrets fixture hid the additional default-rule finding" >&2
	cat "$tmp_dir/gitleaks.json" >&2
	exit 1
fi
echo "controlled custom token and additional GitHub PAT fixture were both rejected"

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

mkdir -p "$tmp_dir/govuln"
cat >"$tmp_dir/govuln/go.mod" <<'EOF'
module example.invalid/reachable-vulnerability-fixture

go 1.26.5

require golang.org/x/text v0.3.6
EOF
cat >"$tmp_dir/govuln/main.go" <<'EOF'
package main

import "golang.org/x/text/language"

func main() {
	_, _ = language.Parse("en-US")
}
EOF
(cd "$tmp_dir/govuln" && go mod tidy)
if ./scripts/run-govulncheck-blocking.sh "$govulncheck" "$tmp_dir/govulncheck-reachable" -C "$tmp_dir/govuln" ./...; then
	echo "reachable govulncheck fixture unexpectedly passed" >&2
	exit 1
fi
if ! grep -q 'GO-2021-0113' "$tmp_dir/govulncheck-reachable.txt"; then
	echo "reachable govulncheck fixture failed without the expected vulnerability" >&2
	cat "$tmp_dir/govulncheck-reachable.txt" >&2
	exit 1
fi
echo "reachable govulncheck fixture rejected by the production blocking wrapper"
