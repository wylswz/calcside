SHELL := /bin/bash

BINARIES := bin/calcside bin/csctl

.PHONY: build test lint web dev serve-dev tidy gen gen-check sdk-test sdk-lint

build: web $(BINARIES)

$(BINARIES): $(shell find cmd internal -name '*.go') web/dist/index.html
	go build -o bin/calcside ./cmd/calcside
	go build -o bin/csctl ./cmd/csctl

# go:embed packages the console; keep the binary in sync with web builds.
web/dist/index.html: web
	@true

web:
	pnpm -C web install --frozen-lockfile
	pnpm -C web build

test:
	go test -race ./...

lint: gen-check
	go vet ./...
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	pnpm -C web typecheck
	pnpm -C web lint

# Generated code (api/openapi.yaml is the source of truth — edit it,
# never the generated files).
gen:
	go generate ./...
	pnpm -C web gen

gen-check: gen
	git diff --exit-code -- internal/api/gen internal/client/gen web/src/api/schema.ts

# DEV ONLY — fixed throwaway key so `make dev` enables the secrets vault.
CALCSIDE_SECRET_KEY ?= MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=

DEV_API_PORT ?= 8787
DEV_DSN ?= calcside-dev.db
export VITE_API_TARGET ?= http://127.0.0.1:$(DEV_API_PORT)

# One command: backend (--dev, anonymous) + Vite frontend. Ctrl-C kills both.
# The backend is prebuilt (fail fast on compile errors) and Vite only
# starts once /healthz answers, so the proxy never serves ECONNREFUSED.
dev:
	@go build -o bin/calcside-dev ./cmd/calcside
	@trap 'kill 0' INT TERM EXIT; \
	( bin/calcside-dev serve --dev --addr 127.0.0.1:$(DEV_API_PORT) \
	    --policy-dir policies/examples --secret-key $(CALCSIDE_SECRET_KEY) \
	    --dsn $(DEV_DSN) ) & \
	backend_pid=$$!; \
	healthy=0; \
	for i in $$(seq 1 240); do \
	  if ! kill -0 $$backend_pid 2>/dev/null; then \
	    echo "make dev: backend exited before becoming healthy" >&2; exit 1; \
	  fi; \
	  if curl -sf http://127.0.0.1:$(DEV_API_PORT)/healthz >/dev/null 2>&1; then \
	    healthy=1; break; \
	  fi; \
	  sleep 0.25; \
	done; \
	if [ $$healthy -ne 1 ]; then \
	  echo "make dev: backend did not answer /healthz within 60s" >&2; exit 1; \
	fi; \
	( if [ ! -d web/node_modules ]; then pnpm -C web install --frozen-lockfile; fi; \
	  pnpm -C web dev ) & \
	wait

# Backend only, serving the embedded console (run `make web` first).
serve-dev:
	go run ./cmd/calcside serve --dev --addr 127.0.0.1:$(DEV_API_PORT) \
	  --policy-dir policies/examples --secret-key $(CALCSIDE_SECRET_KEY) \
	  --dsn $(DEV_DSN)

tidy:
	go mod tidy

# Python SDK (sdk/python): uv project. UV_PROJECT_ENVIRONMENT pins the
# project venv in case a foreign one is activated.
sdk-test:
	cd sdk/python && UV_PROJECT_ENVIRONMENT=$(CURDIR)/sdk/python/.venv \
	  uv run --extra langchain pytest -q

sdk-lint:
	cd sdk/python && UV_PROJECT_ENVIRONMENT=$(CURDIR)/sdk/python/.venv \
	  uv run ruff check . && uv run ruff format --check .
