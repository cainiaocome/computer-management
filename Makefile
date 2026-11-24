SHELL := /bin/bash

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
BIN     := bin
GOFLAGS := -trimpath

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.DEFAULT_GOAL := help
.PHONY: help build test vet fmt fmt-check tidy clean server agent run-server cross e2e

help: ## show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: server agent ## build server and agent for this host

server: ## build the control plane
	@mkdir -p $(BIN)
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN)/fleet-server ./cmd/server

agent: ## build the agent
	@mkdir -p $(BIN)
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN)/fleet-agent ./cmd/agent

test: ## run all tests
	go test ./... -count=1

vet: ## run go vet
	go vet ./...

fmt: ## format all Go code
	gofmt -w .

fmt-check: ## fail if any file is unformatted
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "unformatted:"; echo "$$out"; exit 1; fi

tidy: ## tidy go.mod / go.sum
	go mod tidy

clean: ## remove build output
	rm -rf $(BIN) dist

run-server: ## run the server locally (admin token from FLEET_ADMIN_TOKEN)
	go run ./cmd/server -addr $${FLEET_ADDR:-:8080} -db $${FLEET_DB:-fleet.db}

cross: ## cross-compile the agent for every supported platform into dist/
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=""; \
		[ "$$os" = "windows" ] && ext=".exe"; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -ldflags '$(LDFLAGS)' \
			-o dist/fleet-agent-$$os-$$arch$$ext ./cmd/agent || exit 1; \
	done

e2e: ## run the end-to-end smoke test against a live local server
	bash scripts/e2e.sh
