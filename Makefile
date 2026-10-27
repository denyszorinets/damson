# Damson Makefile. M0 adds build, check, gen, test and dev targets (see CLAUDE.md).

DOCS_DIR := docs
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -X github.com/denyszorinets/damson/internal/version.Version=$(VERSION)
SPHINX   := cd $(DOCS_DIR) && uv run --frozen

.PHONY: build test test-pg gen vet fmt-check check docs docs-serve docs-check

build: ## Build the damson binary into bin/damson
	go build -ldflags "$(LDFLAGS)" -o bin/damson ./cmd/damson

test: ## Run Go tests (SQLite store; Postgres store tests are in test-pg)
	go test ./...

test-pg: ## Store tests against Postgres (testcontainers; DAMSON_TEST_POSTGRES_DSN reuses a server; fails without Docker unless DAMSON_SKIP_POSTGRES=1)
	go test -tags integration -count=1 ./internal/store/...

gen: ## Regenerate sqlc output (version pinned by the go.mod tool directive)
	go tool sqlc generate

vet: ## go vet
	go vet ./...

fmt-check: ## Fail if any Go file is not gofmt-formatted
	@out=$$(gofmt -l cmd internal pkg plugins 2>/dev/null); \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

check: fmt-check vet test ## Go checks that exist so far (lint, Postgres and web join in later M0 issues)

docs: ## Build the documentation site into docs/_build/html
	$(SPHINX) sphinx-build -b html . _build/html

docs-serve: ## Live-reloading docs preview on http://127.0.0.1:8000
	$(SPHINX) sphinx-autobuild --host 127.0.0.1 --port 8000 . _build/html

docs-check: ## Strict docs build (warnings are errors) plus linkcheck
	$(SPHINX) sphinx-build -W --keep-going -n -b html . _build/html
	$(SPHINX) sphinx-build -W --keep-going -b linkcheck . _build/linkcheck
