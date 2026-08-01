#!/bin/sh
set -u

govulncheck=$1
gitleaks=$2
trivy=$3
report_dir=evidence/reports/security
mkdir -p "$report_dir"
status=0

if ! make security-tools; then
	echo "security tool bootstrap failed closed" >&2
	exit 1
fi

./scripts/run-govulncheck-blocking.sh "$govulncheck" "$report_dir/govulncheck" ./...
gate_status=$?
if test "$gate_status" -ne 0; then
	echo "govulncheck rejected the source (exit $gate_status)" >&2
	cat "$report_dir/govulncheck.txt" >&2
	status=1
fi

"$gitleaks" git --no-banner --config .gitleaks.toml --log-opts=HEAD --report-format json --report-path "$report_dir/gitleaks-git.json" .
gate_status=$?
if test "$gate_status" -ne 0; then
	echo "Gitleaks rejected repository history (exit $gate_status)" >&2
	status=1
fi

"$gitleaks" dir --no-banner --config .gitleaks.toml --report-format json --report-path "$report_dir/gitleaks-worktree.json" .
gate_status=$?
if test "$gate_status" -ne 0; then
	echo "Gitleaks rejected the working tree (exit $gate_status)" >&2
	status=1
fi

"$trivy" fs --skip-dirs .tools --skip-dirs evidence/reports --scanners vuln,misconfig --exit-code 0 --format json --output "$report_dir/trivy-filesystem.json" .
gate_status=$?
if test "$gate_status" -ne 0; then
	echo "Trivy could not generate its filesystem/config report (exit $gate_status)" >&2
	status=1
elif ! go run ./cmd/securitypolicy -trivy-report "$report_dir/trivy-filesystem.json"; then
	echo "Trivy findings violate the shared blocking policy" >&2
	status=1
fi

"$trivy" fs --skip-dirs .tools --skip-dirs evidence/reports --scanners vuln,misconfig --severity HIGH,CRITICAL --ignore-unfixed --exit-code 0 --format sarif --output "$report_dir/trivy-filesystem.sarif" .
gate_status=$?
if test "$gate_status" -ne 0; then
	echo "Trivy could not generate SARIF (exit $gate_status)" >&2
	status=1
fi

./scripts/test-negative-security-fixtures.sh "$gitleaks" "$govulncheck"
gate_status=$?
if test "$gate_status" -ne 0; then
	status=1
fi

exit "$status"
