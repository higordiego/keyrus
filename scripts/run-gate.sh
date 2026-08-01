#!/bin/sh
set -u

if test "$#" -lt 3; then
	echo "usage: run-gate.sh CATEGORY GATE COMMAND [ARG...]" >&2
	exit 2
fi

category=$1
gate=$2
shift 2
report_dir="evidence/reports/$category"
log="$report_dir/$gate.log"
metadata="$report_dir/$gate.md"
mkdir -p "$report_dir"

"$@" >"$log" 2>&1
status=$?
cat "$log"

commit=$(git rev-parse HEAD 2>/dev/null || echo unavailable)
dirty=false
test -z "$(git status --porcelain --untracked-files=normal 2>/dev/null)" || dirty=true
result=passed
test "$status" -eq 0 || result=failed

{
	echo "# Gate evidence: $gate"
	echo
	echo "- Commit: \`$commit\`"
	echo "- Working tree dirty: \`$dirty\`"
	echo "- Executed at UTC: \`$(date -u '+%Y-%m-%dT%H:%M:%SZ')\`"
	echo "- Result: **$result** (exit $status)"
	echo
	echo "## Tool versions"
	echo
	echo '```text'
	./scripts/tool-versions.sh
	echo '```'
} >"$metadata"

exit "$status"
