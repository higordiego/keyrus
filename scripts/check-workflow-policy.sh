#!/bin/sh
set -eu

if test "$#" -lt 1; then
	echo "usage: check-workflow-policy.sh WORKFLOW_OR_DIRECTORY [...]" >&2
	exit 2
fi

tmp_list=$(mktemp "${TMPDIR:-/tmp}/keyrus-workflows.XXXXXX")
trap 'rm -f "$tmp_list"' EXIT HUP INT TERM
for input in "$@"; do
	if test -d "$input"; then
		find "$input" -type f \( -name '*.yml' -o -name '*.yaml' \) -print >>"$tmp_list"
	else
		echo "$input" >>"$tmp_list"
	fi
done

status=0
while IFS= read -r file; do
	test -n "$file" || continue
	if test ! -f "$file"; then
		echo "$file: workflow not found" >&2
		status=1
		continue
	fi

	while IFS= read -r line; do
		ref=$(printf '%s\n' "$line" | sed -n 's/^[[:space:]-]*uses:[[:space:]]*["'\'' ]*\([^"'\'' #]*\).*/\1/p')
		test -n "$ref" || continue
		case "$ref" in
			./*) ;;
			docker://*@sha256:*) ;;
			*)
				if ! printf '%s\n' "$ref" | grep -Eq '@[0-9a-f]{40}$'; then
					echo "$file: external action must use a full 40-character commit SHA: $ref" >&2
					status=1
				fi ;;
		esac
	done <"$file"

	if ! grep -q '^permissions:$' "$file" || ! grep -q '^  contents: read$' "$file"; then
		echo "$file: top-level permissions must declare only the read baseline (contents: read)" >&2
		status=1
	fi
	if ! grep -q '^concurrency:$' "$file" || ! grep -q '^  cancel-in-progress: true$' "$file"; then
		echo "$file: concurrency with cancel-in-progress is required" >&2
		status=1
	fi
	if grep -Eq 'pull_request_target:|runs-on:.*self-hosted|id-token:[[:space:]]*write|packages:[[:space:]]*write|attestations:[[:space:]]*write' "$file"; then
		echo "$file: forbidden trigger, runner, or publication permission" >&2
		status=1
	fi
	if grep -Eiq '^[[:space:]]*environment:|secrets\.|docker[[:space:]]+push|docker[[:space:]]+stack[[:space:]]+deploy|kubectl|ghcr\.io' "$file"; then
		echo "$file: environments, secrets, publication, and deployment commands are forbidden in this phase" >&2
		status=1
	fi
	if grep -E '^[[:space:]]+[a-z-]+:[[:space:]]*write([[:space:]]|$)' "$file" | grep -Ev '^[[:space:]]+security-events:[[:space:]]*write([[:space:]]|$)' >/dev/null; then
		echo "$file: only security-events: write is allowed in this phase" >&2
		status=1
	fi
	if ! awk '
		BEGIN { in_jobs=0; have_job=0; timed=0; failed=0 }
		/^jobs:$/ { in_jobs=1; next }
		in_jobs && /^  [A-Za-z0-9_-]+:$/ {
			if (have_job && !timed) failed=1
			have_job=1; timed=0; next
		}
		in_jobs && /^    timeout-minutes: [0-9]+$/ { timed=1 }
		END { if (have_job && !timed) failed=1; exit failed }
	' "$file"; then
		echo "$file: every job must define timeout-minutes" >&2
		status=1
	fi
done <"$tmp_list"

exit "$status"
