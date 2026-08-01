#!/bin/sh
set -eu

if test "$#" -lt 1; then
	echo "usage: check-workflow-policy.sh WORKFLOW_OR_DIRECTORY [...]" >&2
	exit 2
fi

go run ./cmd/workflowpolicy -- "$@"
