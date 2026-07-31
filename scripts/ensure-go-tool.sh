#!/bin/sh
set -eu

if test "$#" -ne 5; then
	echo "usage: ensure-go-tool.sh BIN_DIR VERSION_DIR BINARY VERSION MODULE" >&2
	exit 2
fi

bin_dir=$1
version_dir=$2
binary=$3
version=$4
module=$5
sentinel="$version_dir/$binary-$version"

if test -x "$bin_dir/$binary" && test -f "$sentinel"; then
	exit 0
fi

mkdir -p "$bin_dir" "$version_dir"
GOBIN="$bin_dir" go install "$module@$version"
find "$version_dir" -type f -name "$binary-*" -delete
: > "$sentinel"
