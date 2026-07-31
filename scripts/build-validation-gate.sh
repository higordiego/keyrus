#!/bin/sh
set -eu

trivy=$1
report_dir=evidence/reports/build
bin_dir="$report_dir/bin"
make security-tools
tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/keyrus-build.XXXXXX")
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM
rm -rf "$bin_dir"
mkdir -p "$bin_dir" "$tmp_dir/first" "$tmp_dir/second"

export CGO_ENABLED=0
export SOURCE_DATE_EPOCH
SOURCE_DATE_EPOCH=$(git show -s --format=%ct HEAD)

for package in $(go list ./cmd/...); do
	name=$(basename "$package")
	go build -buildvcs=false -trimpath -ldflags='-buildid=' -o "$tmp_dir/first/$name" "$package"
	go build -buildvcs=false -trimpath -ldflags='-buildid=' -o "$tmp_dir/second/$name" "$package"
	cmp "$tmp_dir/first/$name" "$tmp_dir/second/$name"
	cp "$tmp_dir/first/$name" "$bin_dir/$name"
done

(cd "$bin_dir" && shasum -a 256 * > ../checksums.sha256)
"$trivy" fs --skip-dirs .tools --skip-dirs evidence/reports --format cyclonedx --output "$report_dir/sbom.cyclonedx.json" .
"$trivy" fs --skip-dirs .tools --skip-dirs evidence/reports --format spdx-json --output "$report_dir/sbom.spdx.json" .

if find . -maxdepth 4 -type f \( -name 'Containerfile' -o -name 'Dockerfile' -o -name '*.Containerfile' \) | grep -q .; then
	echo "Container build inputs exist, but no image producer is defined in this foundation ticket." >&2
	echo "Add image construction and scanning atomically in the producer ticket." >&2
	exit 1
fi
cat >"$report_dir/image-scan-not-applicable.json" <<'EOF'
{
  "status": "not-applicable",
  "reason": "No Containerfile or image producer exists in this snapshot; image scanning activates with that producer."
}
EOF

echo "reproducible binaries, SHA-256 checksums, CycloneDX SBOM and SPDX SBOM generated"
