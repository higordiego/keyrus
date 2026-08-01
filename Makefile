SHELL := /bin/sh

GO_VERSION := 1.26.4
TOOLS_BIN := $(CURDIR)/.tools/bin
TOOLS_VERSIONS := $(CURDIR)/.tools/versions
BUF := $(TOOLS_BIN)/buf
BUF_VERSION := v1.57.2
PROTOC_GEN_GO_VERSION := v1.36.9
PROTOC_GEN_GO_GRPC_VERSION := v1.6.2
GRPC_GATEWAY_VERSION := v2.27.3

export PATH := $(TOOLS_BIN):$(PATH)

.PHONY: all bootstrap check-go-version tools deps format format-check generate generate-check baseline proto-lint proto-breaking build lint test integration bdd bdd-parse reports ci clean-reports

all: ci

bootstrap: check-go-version tools deps generate
	$(MAKE) build

check-go-version:
	@test "$$(go env GOVERSION)" = "go$(GO_VERSION)" || { echo "Go $(GO_VERSION) is required; found $$(go env GOVERSION)" >&2; exit 1; }

tools:
	@./scripts/ensure-go-tool.sh "$(TOOLS_BIN)" "$(TOOLS_VERSIONS)" buf "$(BUF_VERSION)" github.com/bufbuild/buf/cmd/buf
	@./scripts/ensure-go-tool.sh "$(TOOLS_BIN)" "$(TOOLS_VERSIONS)" protoc-gen-go "$(PROTOC_GEN_GO_VERSION)" google.golang.org/protobuf/cmd/protoc-gen-go
	@./scripts/ensure-go-tool.sh "$(TOOLS_BIN)" "$(TOOLS_VERSIONS)" protoc-gen-go-grpc "$(PROTOC_GEN_GO_GRPC_VERSION)" google.golang.org/grpc/cmd/protoc-gen-go-grpc
	@./scripts/ensure-go-tool.sh "$(TOOLS_BIN)" "$(TOOLS_VERSIONS)" protoc-gen-grpc-gateway "$(GRPC_GATEWAY_VERSION)" github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway
	@./scripts/ensure-go-tool.sh "$(TOOLS_BIN)" "$(TOOLS_VERSIONS)" protoc-gen-openapiv2 "$(GRPC_GATEWAY_VERSION)" github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2

deps:
	go mod download

format: tools
	gofmt -w $$(find . -type f -name '*.go' -not -path './.tools/*')
	$(BUF) format -w

format-check: tools
	@unformatted=$$(gofmt -l $$(find . -type f -name '*.go' -not -path './.tools/*')); \
	if test -n "$$unformatted"; then echo "Go files need formatting:"; echo "$$unformatted"; exit 1; fi
	$(BUF) format --diff --exit-code

generate: tools
	$(BUF) generate
	$(BUF) generate --template buf.gen.openapi.yaml --path proto/cashflow/ledger/public/v1 --path proto/cashflow/consolidation/public/v1
	mkdir -p api/descriptors
	$(BUF) build -o api/descriptors/current.binpb

generate-check: tools
	./scripts/check-generated.sh

baseline: tools
	mkdir -p api/descriptors
	$(BUF) build -o api/descriptors/baseline.binpb

proto-lint: tools
	$(BUF) lint

proto-breaking: tools
	$(BUF) breaking --against api/descriptors/baseline.binpb

build:
	go build ./...

lint: proto-lint
	go vet ./...

test:
	go test -race -count=1 ./...

integration:
	@mkdir -p evidence/reports/integration
	@report=evidence/reports/integration/postgresql.log; \
	go test -race -count=1 -v ./services/consolidation/internal/adapters/outbound/postgres ./test/bdd > "$$report" 2>&1; \
	status=$$?; cat "$$report"; exit $$status

bdd:
	go test -race -count=1 -v ./test/bdd

bdd-parse:
	go run ./cmd/bddcheck -features features -manifest features/implemented_scenarios.txt

clean-reports:
	rm -f evidence/reports/go-test.json evidence/reports/bdd-catalog.json

reports:
	mkdir -p evidence/reports
	go test -race -json ./... > evidence/reports/go-test.json
	go run ./cmd/bddcheck -features features -manifest features/implemented_scenarios.txt -json > evidence/reports/bdd-catalog.json

ci: check-go-version generate-check format-check lint proto-breaking build test integration bdd-parse
