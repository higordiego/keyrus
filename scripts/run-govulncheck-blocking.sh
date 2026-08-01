#!/bin/sh
set -u

if test "$#" -lt 3; then
	echo "usage: run-govulncheck-blocking.sh GOVULNCHECK REPORT_PREFIX GOVULNCHECK_ARG [...]" >&2
	exit 2
fi

govulncheck=$1
report_prefix=$2
shift 2

# Text mode is the blocking execution: govulncheck returns 3 for reachable
# vulnerabilities. JSON mode is generated separately because it can return 0
# while still containing reachable call traces.
"$govulncheck" "$@" >"$report_prefix.txt" 2>&1
blocking_status=$?

"$govulncheck" -json "$@" >"$report_prefix.json" 2>"$report_prefix.json.stderr.log"
json_status=$?

if test "$blocking_status" -ne 0; then
	exit "$blocking_status"
fi
if test "$json_status" -ne 0; then
	echo "govulncheck JSON evidence generation failed with exit $json_status" >&2
	exit "$json_status"
fi
