#!/bin/sh
# Renders the versioned Keycloak realm template into an import file.
#
# The template in Git never contains a credential. Every secret is injected here
# from the environment or from a Docker secret mounted under /run/secrets, and
# the rendered output is written outside version control.
#
# Usage: render-realm.sh <output-path>
set -eu

TEMPLATE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/realm-cashflow.json"
OUTPUT="${1:-}"

if [ -z "$OUTPUT" ]; then
	echo "usage: render-realm.sh <output-path>" >&2
	exit 2
fi

PLACEHOLDERS="CASHFLOW_CONSOLIDATION_CLIENT_SECRET CASHFLOW_RECONCILIATION_CLIENT_SECRET CASHFLOW_MERCHANT_A_PASSWORD CASHFLOW_MERCHANT_B_PASSWORD"

# Docker secrets take precedence over the environment so the value never has to
# transit through a process listing or a Compose file.
for name in $PLACEHOLDERS; do
	secret_file="/run/secrets/$(echo "$name" | tr '[:upper:]' '[:lower:]')"
	if [ -r "$secret_file" ]; then
		# export accepts NAME=VALUE as one argument. This avoids evaluating
		# credential bytes as shell syntax while still supporting dynamic names.
		export "$name=$(cat "$secret_file")"
	fi
done

rendered=$(cat "$TEMPLATE")
for name in $PLACEHOLDERS; do
	# printenv provides safe indirection: a secret containing command
	# substitutions, whitespace or shell metacharacters remains inert data.
	value=$(printenv "$name" 2>/dev/null || true)
	if [ -z "$value" ]; then
		echo "render-realm.sh: $name is unset; refusing to emit a realm with an empty credential" >&2
		exit 1
	fi
	case "$value" in
	*\"* | *\\*)
		echo "render-realm.sh: $name contains a quote or backslash and cannot be embedded safely" >&2
		exit 1
		;;
	esac
	if [ "$(printf '%s' "$value" | wc -l | tr -d ' ')" != "0" ]; then
		echo "render-realm.sh: $name spans multiple lines and cannot be embedded safely" >&2
		exit 1
	fi
	rendered=$(printf '%s' "$rendered" | REPLACEMENT="$value" PLACEHOLDER="\${$name}" awk '
		BEGIN { placeholder = ENVIRON["PLACEHOLDER"]; replacement = ENVIRON["REPLACEMENT"] }
		{
			line = $0
			out = ""
			while ((idx = index(line, placeholder)) > 0) {
				out = out substr(line, 1, idx - 1) replacement
				line = substr(line, idx + length(placeholder))
			}
			print out line
		}
	')
done

case "$rendered" in
*'${'*)
	echo "render-realm.sh: unresolved placeholder remains in the rendered realm" >&2
	exit 1
	;;
esac

umask 077
temporary=$(mktemp "${OUTPUT}.tmp.XXXXXX")
cleanup() {
	rm -f -- "$temporary"
}
trap cleanup EXIT HUP INT TERM
chmod 0600 "$temporary"
printf '%s\n' "$rendered" >"$temporary"
# The temporary file lives beside the destination, so rename is atomic. It also
# replaces a permissive pre-existing file or symlink instead of inheriting its
# mode while truncating it in place.
mv -f -- "$temporary" "$OUTPUT"
trap - EXIT HUP INT TERM
echo "render-realm.sh: wrote $OUTPUT"
