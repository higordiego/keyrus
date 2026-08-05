#!/bin/sh
set -eu

repository=higordiego/keyrus
branch=main
mode=check
licensed=true

for arg in "$@"; do
	case "$arg" in
		--apply) mode=apply ;;
		--core-only) licensed=false ;;
		*) echo "usage: apply-main-protection.sh [--apply] [--core-only]" >&2; exit 2 ;;
	esac
done

if ! command -v gh >/dev/null 2>&1; then
	echo "GitHub CLI (gh) is required for this administrative helper" >&2
	exit 1
fi

if test "$mode" = check; then
	echo "Reading protection for $repository:$branch (no changes)"
	gh api -H 'Accept: application/vnd.github+json' "repos/$repository/branches/$branch/protection"
	exit $?
fi

if test "$licensed" = true; then
	contexts='["foundation","workflow-policy","source-security","dependency-review","codeql-go-kotlin","codeql-actions"]'
else
	contexts='["foundation","workflow-policy","source-security"]'
fi

payload=$(mktemp "${TMPDIR:-/tmp}/keyrus-main-protection.XXXXXX")
trap 'rm -f "$payload"' EXIT HUP INT TERM
{
	echo '{'
	echo '  "required_status_checks": {'
	echo '    "strict": true,'
	printf '    "contexts": %s\n' "$contexts"
	echo '  },'
	echo '  "enforce_admins": true,'
	echo '  "required_pull_request_reviews": {'
	echo '    "dismiss_stale_reviews": true,'
	echo '    "require_code_owner_reviews": false,'
	echo '    "required_approving_review_count": 1,'
	echo '    "require_last_push_approval": true'
	echo '  },'
	echo '  "restrictions": null,'
	echo '  "required_linear_history": true,'
	echo '  "allow_force_pushes": false,'
	echo '  "allow_deletions": false,'
	echo '  "block_creations": false,'
	echo '  "required_conversation_resolution": true,'
	echo '  "lock_branch": false,'
	echo '  "allow_fork_syncing": true'
	echo '}'
} >"$payload"

echo "Applying idempotent protection to $repository:$branch"
gh api --method PUT \
	-H 'Accept: application/vnd.github+json' \
	-H 'X-GitHub-Api-Version: 2022-11-28' \
	"repos/$repository/branches/$branch/protection" \
	--input "$payload"
