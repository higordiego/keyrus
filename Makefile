SHELL := /bin/sh

GO_VERSION := 1.26.5
TOOLS_BIN := $(CURDIR)/.tools/bin
TOOLS_VERSIONS := $(CURDIR)/.tools/versions
BUF := $(TOOLS_BIN)/buf
ACTIONLINT := $(TOOLS_BIN)/actionlint
GOVULNCHECK := $(TOOLS_BIN)/govulncheck
GITLEAKS := $(TOOLS_BIN)/gitleaks
TRIVY := $(TOOLS_BIN)/trivy
BUF_VERSION := v1.57.2
PROTOC_GEN_GO_VERSION := v1.36.9
PROTOC_GEN_GO_GRPC_VERSION := v1.6.2
GRPC_GATEWAY_VERSION := v2.27.3
ACTIONLINT_VERSION := v1.7.9
GOVULNCHECK_VERSION := v1.6.0
GITLEAKS_VERSION := v8.30.1
TRIVY_VERSION := v0.72.0

export PATH := $(TOOLS_BIN):$(PATH)

.PHONY: all bootstrap check-go-version tools policy-tools security-tools deps format format-check generate generate-check baseline proto-lint proto-breaking build lint test bdd-parse reports ci policy security build-validation full-validation clean-reports

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

policy-tools: check-go-version
	@./scripts/ensure-go-tool.sh "$(TOOLS_BIN)" "$(TOOLS_VERSIONS)" actionlint "$(ACTIONLINT_VERSION)" github.com/rhysd/actionlint/cmd/actionlint

security-tools: check-go-version
	@./scripts/ensure-go-tool.sh "$(TOOLS_BIN)" "$(TOOLS_VERSIONS)" govulncheck "$(GOVULNCHECK_VERSION)" golang.org/x/vuln/cmd/govulncheck
	@./scripts/ensure-release-tool.sh "$(TOOLS_BIN)" "$(TOOLS_VERSIONS)" gitleaks "$(GITLEAKS_VERSION)"
	@./scripts/ensure-release-tool.sh "$(TOOLS_BIN)" "$(TOOLS_VERSIONS)" trivy "$(TRIVY_VERSION)"

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
	go test -race ./...

bdd-parse:
	go run ./cmd/bddcheck -features features -manifest features/implemented_scenarios.txt

clean-reports:
	find evidence/reports -mindepth 1 ! -name .gitkeep -delete

reports:
	./scripts/ci-reports.sh

ci: check-go-version generate-check format-check lint proto-breaking build test bdd-parse

policy: check-go-version
	./scripts/run-gate.sh policy policy ./scripts/policy-gate.sh "$(ACTIONLINT)"

security: check-go-version
	./scripts/run-gate.sh security security ./scripts/security-gate.sh "$(GOVULNCHECK)" "$(GITLEAKS)" "$(TRIVY)"

build-validation: check-go-version
	./scripts/run-gate.sh build build-validation ./scripts/build-validation-gate.sh "$(TRIVY)"

full-validation: check-go-version
	./scripts/run-gate.sh full full-validation ./scripts/full-validation-gate.sh
