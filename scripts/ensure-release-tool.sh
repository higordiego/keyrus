#!/bin/sh
set -eu

if test "$#" -ne 4; then
	echo "usage: ensure-release-tool.sh BIN_DIR VERSION_DIR TOOL VERSION" >&2
	exit 2
fi

bin_dir=$1
version_dir=$2
tool=$3
version=$4
sentinel="$version_dir/$tool-$version"

if test -x "$bin_dir/$tool" && test -f "$sentinel"; then
	exit 0
fi

os=$(uname -s)
arch=$(uname -m)
case "$os/$arch" in
	Darwin/arm64) platform=darwin-arm64 ;;
	Darwin/x86_64) platform=darwin-amd64 ;;
	Linux/x86_64) platform=linux-amd64 ;;
	Linux/aarch64|Linux/arm64) platform=linux-arm64 ;;
	*) echo "unsupported platform for $tool: $os/$arch" >&2; exit 1 ;;
esac

case "$tool/$version/$platform" in
	gitleaks/v8.30.1/darwin-arm64)
		asset=gitleaks_8.30.1_darwin_arm64.tar.gz
		checksum=b40ab0ae55c505963e365f271a8d3846efbc170aa17f2607f13df610a9aeb6a5 ;;
	gitleaks/v8.30.1/darwin-amd64)
		asset=gitleaks_8.30.1_darwin_x64.tar.gz
		checksum=dfe101a4db2255fc85120ac7f3d25e4342c3c20cf749f2c20a18081af1952709 ;;
	gitleaks/v8.30.1/linux-amd64)
		asset=gitleaks_8.30.1_linux_x64.tar.gz
		checksum=551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb ;;
	gitleaks/v8.30.1/linux-arm64)
		asset=gitleaks_8.30.1_linux_arm64.tar.gz
		checksum=e4a487ee7ccd7d3a7f7ec08657610aa3606637dab924210b3aee62570fb4b080 ;;
	trivy/v0.72.0/darwin-arm64)
		asset=trivy_0.72.0_macOS-ARM64.tar.gz
		checksum=88f208680dc05da2b459e19b4f5aa2b4dc7c2117892ba4aab2ae63baba330016 ;;
	trivy/v0.72.0/darwin-amd64)
		asset=trivy_0.72.0_macOS-64bit.tar.gz
		checksum=ee5e60df8a98e5b89fd74a6d86f9e5c7e9a266a35002cb1e43291698b3bfee08 ;;
	trivy/v0.72.0/linux-amd64)
		asset=trivy_0.72.0_Linux-64bit.tar.gz
		checksum=bbb64b9695866ce4a7a8f5c9592002c5961cab378577fa3f8a040df362b9b2ea ;;
	trivy/v0.72.0/linux-arm64)
		asset=trivy_0.72.0_Linux-ARM64.tar.gz
		checksum=2ca2c023109c2db6b2b77366b6717291452d4531167377d95c79547f0c8e3467 ;;
	*) echo "unsupported or unverified release: $tool $version on $platform" >&2; exit 1 ;;
esac

case "$tool" in
	gitleaks) url="https://github.com/gitleaks/gitleaks/releases/download/$version/$asset" ;;
	trivy) url="https://github.com/aquasecurity/trivy/releases/download/$version/$asset" ;;
esac

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/keyrus-$tool.XXXXXX")
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM
curl --proto '=https' --tlsv1.2 -fsSLo "$tmp_dir/$asset" "$url"
actual=$(shasum -a 256 "$tmp_dir/$asset" | awk '{print $1}')
if test "$actual" != "$checksum"; then
	echo "$tool archive checksum mismatch: expected $checksum, got $actual" >&2
	exit 1
fi
tar -xzf "$tmp_dir/$asset" -C "$tmp_dir" "$tool"
mkdir -p "$bin_dir" "$version_dir"
cp "$tmp_dir/$tool" "$bin_dir/$tool"
chmod 0755 "$bin_dir/$tool"
find "$version_dir" -type f -name "$tool-*" -delete
: > "$sentinel"
