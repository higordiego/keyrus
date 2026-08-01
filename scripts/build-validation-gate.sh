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

for package in $(go list -f '{{if eq .Name "main"}}{{.ImportPath}}{{end}}' ./cmd/... ./services/... | sed '/^$/d'); do
	name=$(basename "$package")
	go build -buildvcs=false -trimpath -ldflags='-buildid=' -o "$tmp_dir/first/$name" "$package"
	go build -buildvcs=false -trimpath -ldflags='-buildid=' -o "$tmp_dir/second/$name" "$package"
	cmp "$tmp_dir/first/$name" "$tmp_dir/second/$name"
	cp "$tmp_dir/first/$name" "$bin_dir/$name"
done

(cd "$bin_dir" && shasum -a 256 * > ../checksums.sha256)
"$trivy" fs --skip-dirs .tools --skip-dirs evidence/reports --format cyclonedx --output "$report_dir/sbom.cyclonedx.json" .
"$trivy" fs --skip-dirs .tools --skip-dirs evidence/reports --format spdx-json --output "$report_dir/sbom.spdx.json" .

command -v docker >/dev/null 2>&1 || {
	echo "Docker is required to build and scan the outbox publisher image" >&2
	exit 1
}
image=keyrus/outbox-publisher:validation
docker build --file services/ledger/Containerfile.outbox-publisher --tag "$image" .
docker image inspect --format '{{.Id}}' "$image" >"$report_dir/outbox-publisher.image-id"
"$trivy" image --scanners vuln --format json --output "$report_dir/outbox-publisher.image-scan.json" "$image"
go run ./cmd/securitypolicy -trivy-report "$report_dir/outbox-publisher.image-scan.json"
"$trivy" image --format cyclonedx --output "$report_dir/outbox-publisher.sbom.cyclonedx.json" "$image"
"$trivy" image --format spdx-json --output "$report_dir/outbox-publisher.sbom.spdx.json" "$image"

echo "reproducible binaries, outbox image, SHA-256 checksums, SBOMs and blocking image scan generated"
