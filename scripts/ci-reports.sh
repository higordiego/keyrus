#!/bin/sh
set -u

report_dir=evidence/reports/ci
mkdir -p "$report_dir"
status=0
go test -race -json ./... >"$report_dir/go-test.json"
test_status=$?
test "$test_status" -eq 0 || status=1
go run ./cmd/bddcheck -features features -manifest features/implemented_scenarios.txt -json >"$report_dir/bdd-catalog.json"
bdd_status=$?
test "$bdd_status" -eq 0 || status=1

commit=$(git rev-parse HEAD 2>/dev/null || echo unavailable)
{
	echo "# CI report metadata"
	echo
	echo "- Commit: \`$commit\`"
	echo "- Executed at UTC: \`$(date -u '+%Y-%m-%dT%H:%M:%SZ')\`"
	echo "- go test exit: \`$test_status\`"
	echo "- BDD catalog exit: \`$bdd_status\`"
	echo
	echo '```text'
	./scripts/tool-versions.sh
	echo '```'
} >"$report_dir/metadata.md"

exit "$status"
