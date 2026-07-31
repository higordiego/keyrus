#!/bin/sh
set -eu

# The foundation snapshot has no external datastore/broker producer yet. Its real
# integration surface is the generated API contract plus executable Godog wiring.
go test -race ./api ./test/bdd
go run ./cmd/bddcheck -features features -manifest features/implemented_scenarios.txt
