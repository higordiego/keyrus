#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
snapshot_dir=$(mktemp -d "${TMPDIR:-/tmp}/keyrus-generated.XXXXXX")
trap 'rm -rf "$snapshot_dir"' EXIT HUP INT TERM

cd "$repo_root"

for path in gen/go api/openapi api/descriptors/current.binpb; do
	if test ! -e "$path"; then
		echo "generated artifact missing before clean-diff check: $path" >&2
		exit 1
	fi
	mkdir -p "$snapshot_dir/$(dirname "$path")"
	cp -R "$path" "$snapshot_dir/$path"
done

make generate

for path in gen/go api/openapi api/descriptors/current.binpb; do
	diff -ru "$snapshot_dir/$path" "$path"
done

if git rev-parse --verify HEAD >/dev/null 2>&1; then
	git diff --exit-code -- gen/go api/openapi api/descriptors/current.binpb
fi

