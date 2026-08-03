#!/bin/sh
set -u

report_dir=evidence/reports/ci
mkdir -p "$report_dir"

./scripts/generate-reports.sh "$report_dir"
report_status=$?

commit=$(git rev-parse HEAD 2>/dev/null || echo unavailable)
{
	echo "# CI report metadata"
	echo
	echo "- Commit: \`$commit\`"
	echo "- Executed at UTC: \`$(date -u '+%Y-%m-%dT%H:%M:%SZ')\`"
	echo "- Producers exit: \`$report_status\`"
	echo
	echo '```text'
	./scripts/tool-versions.sh
	echo '```'
} >"$report_dir/metadata.md"

exit "$report_status"
