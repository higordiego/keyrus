#!/bin/sh
set -u

if test "$#" -ne 1; then
	echo "usage: $0 REPORTS_DIR" >&2
	exit 2
fi

reports_dir=$1
mkdir -p "$reports_dir"
rm -f "$reports_dir/go-test.json" "$reports_dir/bdd-catalog.json"

go_test_status=0
go test -race -json ./... >"$reports_dir/go-test.json" || go_test_status=$?

bdd_catalog_status=0
go run ./cmd/bddcheck -features features -manifest features/implemented_scenarios.txt -json >"$reports_dir/bdd-catalog.json" || bdd_catalog_status=$?

if test "$go_test_status" -ne 0 || test "$bdd_catalog_status" -ne 0; then
	echo "report producers failed: go-test=$go_test_status bdd-catalog=$bdd_catalog_status" >&2
	if test "$go_test_status" -ne 0; then
		exit "$go_test_status"
	fi
	exit "$bdd_catalog_status"
fi
