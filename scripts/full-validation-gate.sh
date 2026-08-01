#!/bin/sh
set -u

status=0
for gate in policy ci security build-validation; do
	echo "==> make $gate"
	make "$gate"
	gate_status=$?
	if test "$gate_status" -ne 0; then
		echo "$gate failed with exit $gate_status" >&2
		status=1
	fi
	if test "$gate" = ci; then
		echo "==> make reports"
		make reports || status=1
	fi
done
exit "$status"
