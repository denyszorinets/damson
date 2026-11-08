# Damson Makefile. M0 adds build, check, gen, test and dev targets (see CLAUDE.md).

DOCS_DIR := docs
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -X github.com/denyszorinets/damson/internal/version.Version=$(VERSION)
# Go does not ignore node_modules; list packages explicitly.
GOPKGS   := $(shell go list ./... | grep -v /web/node_modules/)
# Extra `go test` flags, e.g. `make test GOTESTFLAGS=-race` (CI uses -race and -v).
GOTESTFLAGS ?=
SPHINX   := cd $(DOCS_DIR) && uv run --frozen

.PHONY: systemd-check bundle helm-check chaos test-integration plugin-contract build test test-pg gen vet fmt-check lint check docs docs-serve docs-check web web-check

build: ## Build the damson binary into bin/damson
	go build -ldflags "$(LDFLAGS)" -o bin/damson ./cmd/damson

test: ## Run Go tests (SQLite store; Postgres store tests are in test-pg)
	go test $(GOTESTFLAGS) $(GOPKGS)

# Every package with `//go:build integration` tests must be listed here, or CI never runs them.
PGPKGS := ./internal/store/... ./internal/api/... ./internal/controller/... ./internal/agent/... ./internal/cli/...

test-pg: ## Integration tests against Postgres: store, api, controller, agent, cli (testcontainers; DAMSON_TEST_POSTGRES_DSN reuses a server; fails without Docker unless DAMSON_SKIP_POSTGRES=1)
	go test $(GOTESTFLAGS) -tags integration -count=1 $(PGPKGS)

chaos: ## HA chaos tests: several in-process controllers on one Postgres, agents moved and cut off (testcontainers; DAMSON_TEST_POSTGRES_DSN reuses a server)
	go test $(GOTESTFLAGS) -tags chaos -race -count=1 -timeout 10m ./internal/chaos/...

# Packages with `//go:build integration` tests that do not use Postgres (they need Docker for other containers).
TESTINTPKGS := ./plugins/ssh/... ./internal/secrets/openbao/...

test-integration: ## Non-Postgres integration tests (plugins/ssh against an sshd container); needs Docker and fails without it; list new packages in TESTINTPKGS
	go test $(GOTESTFLAGS) -tags integration -count=1 $(TESTINTPKGS)

gen: ## Regenerate sqlc output, web/openapi.json, the job spec JSON Schema and the TypeScript API client (needs node; runs npm ci if web/node_modules is missing)
	go tool sqlc generate
	go run ./internal/api/genopenapi web/openapi.json
	go run ./internal/engine/genschema testdata/engine/job-v1.schema.json
	cd web && { [ -d node_modules ] || npm ci; } && npm run gen:api

vet: ## go vet
	go vet $(GOPKGS)

fmt-check: ## Fail if any Go file is not gofmt-formatted
	@out=$$(gofmt -l cmd internal pkg plugins examples 2>/dev/null); \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

lint: ## golangci-lint (config in .golangci.yml); fails if not installed
	golangci-lint run ./...

check: fmt-check vet lint-maybe test web-check-maybe ## gofmt, vet, golangci-lint and tests (SQLite), plus web lint and typecheck when web/node_modules exists; Postgres tests are in test-pg

.PHONY: lint-maybe
lint-maybe:
ifneq ($(shell command -v golangci-lint 2>/dev/null),)
	@$(MAKE) --no-print-directory lint
else ifneq ($(filter-out 0 false,$(CI)),)
	@echo "FAILED lint: golangci-lint is not installed and CI is set"; exit 1
else
	@echo "SKIPPED lint (install golangci-lint v2 to enable)"
endif

.PHONY: web-check-maybe
web-check-maybe:
ifneq ($(wildcard web/node_modules),)
	@$(MAKE) --no-print-directory web-check
else ifneq ($(filter-out 0 false,$(CI)),)
	@echo "FAILED web-check: web/node_modules is missing and CI is set (run make web)"; exit 1
else
	@echo "SKIPPED web-check (run make web)"
endif

web: ## Build the SvelteKit SPA into web/build (embedded by the binary)
	cd web && npm ci && npm run build

web-check: ## Web lint (prettier + eslint), typecheck (svelte-check) and unit tests (vitest)
	cd web && npm run lint && npm run check && npm run test:unit

helm-check: ## Lint and render the Helm chart (deploy/helm) with every ci/ values file; needs helm on PATH
	@command -v helm >/dev/null 2>&1 || { echo "helm-check: helm is not installed"; exit 1; }
	sh deploy/helm/check.sh

systemd-check: ## Verify the systemd units with systemd-analyze and check their hardening
	@command -v systemd-analyze >/dev/null 2>&1 || { echo "systemd-check: systemd-analyze is not installed"; exit 1; }
	sh deploy/systemd/check.sh

bundle: ## Build the offline bundle into dist/ (binaries, chart, units, checksums; WITH_IMAGES=1 adds the images). Needs web/build and helm
	sh deploy/bundle/build.sh

docs: ## Build the documentation site into docs/_build/html
	$(SPHINX) sphinx-build -b html . _build/html

docs-serve: ## Live-reloading docs preview on http://127.0.0.1:8000
	$(SPHINX) sphinx-autobuild --host 127.0.0.1 --port 8000 . _build/html

docs-check: ## Strict docs build (warnings are errors) plus linkcheck
	$(SPHINX) sphinx-build -W --keep-going -n -b html . _build/html
	$(SPHINX) sphinx-build -W --keep-going -b linkcheck . _build/linkcheck

plugin-contract: ## Plugin SDK contract suite over all built-in plugins, plus build examples/plugins/hello (a separate module) into a custom binary with damson build --with (offline)
	go test ./pkg/pluginsdk/... ./plugins/...
	cd examples/plugins/hello && go vet ./... && go test ./...
	go mod download
	cd examples/plugins/hello && go mod download
	go build -o bin/damson ./cmd/damson
	GOPROXY=off bin/damson build \
		--with github.com/denyszorinets/damson=$(CURDIR) \
		--with github.com/denyszorinets/damson/examples/plugins/hello=$(CURDIR)/examples/plugins/hello \
		-o bin/damson-hello
	bin/damson-hello plugin validate hello
	echo '{"name":"ci"}' | bin/damson-hello plugin run hello --input - | grep -q '"greeting": "Hello, ci!"'
	bin/damson-hello version | grep -q 'examples/plugins/hello'
