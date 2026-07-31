#!/bin/sh
set -eu

go version
sed -n 's/^\([A-Z0-9_]*_VERSION\) := \(.*\)$/\1=\2/p' Makefile
if test -x .tools/bin/trivy; then
	.tools/bin/trivy --version
fi
