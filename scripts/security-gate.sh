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

"$govulncheck" -json ./... >"$report_dir/govulncheck.json" 2>"$report_dir/govulncheck.stderr.log"
gate_status=$?
if test "$gate_status" -ne 0; then
	echo "govulncheck rejected the source (exit $gate_status)" >&2
	cat "$report_dir/govulncheck.stderr.log" >&2
	status=1
fi

"$gitleaks" git --no-banner --config .gitleaks.toml --report-format json --report-path "$report_dir/gitleaks-git.json" .
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

"$trivy" fs --skip-dirs .tools --skip-dirs evidence/reports --scanners vuln,misconfig --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1 --format json --output "$report_dir/trivy-filesystem.json" .
gate_status=$?
if test "$gate_status" -ne 0; then
	echo "Trivy rejected filesystem/config HIGH or CRITICAL findings with fixes (exit $gate_status)" >&2
	status=1
fi

"$trivy" fs --skip-dirs .tools --skip-dirs evidence/reports --scanners vuln,misconfig --severity HIGH,CRITICAL --ignore-unfixed --exit-code 0 --format sarif --output "$report_dir/trivy-filesystem.sarif" .
gate_status=$?
if test "$gate_status" -ne 0; then
	echo "Trivy could not generate SARIF (exit $gate_status)" >&2
	status=1
fi

./scripts/test-negative-security-fixtures.sh "$gitleaks"
gate_status=$?
if test "$gate_status" -ne 0; then
	status=1
fi

exit "$status"
