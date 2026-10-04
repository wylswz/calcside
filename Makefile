SHELL := /bin/bash

BINARIES := bin/calcside bin/csctl bin/calcside-worker

.PHONY: build test test-cgroup test-cgroup-host lint web dev serve-dev tidy gen gen-check migrate-diff migrate-hash sdk-test sdk-lint docker-env

# First-run compose setup: generate docker/.env with a random shared
# key, or copy docker/.env.example to fill in yourself.
docker-env:
	@if [ -f docker/.env ]; then echo "docker/.env already exists"; exit 0; fi; \
	echo "CALCSIDE_WORKER_SHARED_KEY=$$(openssl rand -base64 32)" > docker/.env; \
	echo "wrote docker/.env (gitignored)"

build: web $(BINARIES)

$(BINARIES): $(shell find cmd internal -name '*.go') web/dist/index.html
	go build -o bin/calcside ./cmd/calcside
	go build -o bin/csctl ./cmd/csctl
	go build -o bin/calcside-worker ./cmd/calcside-worker

# go:embed packages the console; keep the binary in sync with web builds.
web/dist/index.html: web
	@true

web:
	pnpm -C web install --frozen-lockfile
	pnpm -C web build

test:
	go test -race ./...

# Per-instance cgroup memory limit test (Linux, root). Runs the test binary
# in a throwaway privileged container; the container's processes move to
# /init first, since cgroup v2 only delegates controllers from a group
# that has no member processes. The supervisor never writes above its
# parent group, so memory is enabled at the root here.
test-cgroup:
	CGO_ENABLED=0 GOOS=linux GOARCH=$$(docker version -f '{{.Server.Arch}}') \
	  go test -c -o bin/subproc.linux.test ./internal/node/subproc
	docker run --rm --privileged --cgroupns=private -v $(CURDIR)/bin:/t:ro \
	  --entrypoint sh alpine:3.22 -c '\
	    mkdir /sys/fs/cgroup/init && \
	    for p in $$(cat /sys/fs/cgroup/cgroup.procs); do echo $$p > /sys/fs/cgroup/init/cgroup.procs 2>/dev/null; done; \
	    echo +memory > /sys/fs/cgroup/cgroup.subtree_control && \
	    CALCSIDE_TEST_CGROUP=1 CALCSIDE_TEST_CGROUP_PARENT=/calcside-test \
	      exec /t/subproc.linux.test -test.v -test.run Cgroup'

# Same test straight on a Linux host via sudo, with -race. The parent sits
# under the real root cgroup, which is exempt from that constraint
# (systemd normally enables memory there already).
test-cgroup-host:
	go test -race -c -o bin/subproc.test ./internal/node/subproc
	sudo sh -c 'echo +memory > /sys/fs/cgroup/cgroup.subtree_control'
	sudo env CALCSIDE_TEST_CGROUP=1 CALCSIDE_TEST_CGROUP_PARENT=/calcside-test \
	  bin/subproc.test -test.v -test.run Cgroup

lint: gen-check
	go vet ./...
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	pnpm -C web typecheck
	pnpm -C web lint

# Generated code (api/openapi.yaml and api/worker.openapi.yaml are the
# source of truth — edit them, never the generated files).
gen:
	go generate ./...
	pnpm -C web gen

gen-check: gen
	git diff --exit-code -- internal/api/gen internal/api/intgen internal/client/gen internal/runtime/remote/gen internal/runtime/remote/apiclient web/src/api/schema.ts

# Store schema migrations (Atlas, see atlas.hcl). The GORM row models are
# the source of truth; migrate-diff writes a new versioned file from model
# changes. Requires the atlas CLI.
migrate-diff:
	@test -n "$(name)" || (echo "usage: make migrate-diff name=<desc>" >&2; exit 1)
	atlas migrate diff --env sqlite $(name)

migrate-hash:
	atlas migrate hash --env sqlite

# DEV ONLY — fixed throwaway key so `make dev` enables the secrets vault.
CALCSIDE_SECRET_KEY ?= MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=

DEV_API_PORT ?= 8787
DEV_WORKER_PORT ?= 8090
DEV_WORKER_ID ?= dev-w1
# DEV ONLY — fixed throwaway key for the API↔worker shared secret.
DEV_WORKER_KEY ?= dev-worker-key-not-secret
DEV_DSN ?= calcside-dev.db
DEV_EXT_ROOTS ?= $(CURDIR)/examples/capabilities
DEV_NET_ALLOW_CIDRS ?= 198.18.0.0/15
export VITE_API_TARGET ?= http://127.0.0.1:$(DEV_API_PORT)

# One command: worker + API (--dev, anonymous, remote exec tier) + Vite
# frontend. Ctrl-C kills all three. Binaries are prebuilt (fail fast on
# compile errors); the API starts once the worker is healthy and Vite
# once the API is, so nothing proxy-serves ECONNREFUSED.
dev:
	@go build -o bin/calcside-dev ./cmd/calcside
	@go build -o bin/calcside-worker-dev ./cmd/calcside-worker
	@trap 'kill 0' INT TERM EXIT; \
	( bin/calcside-worker-dev serve --listen 127.0.0.1:$(DEV_WORKER_PORT) \
	    --node-id $(DEV_WORKER_ID) --shared-key $(DEV_WORKER_KEY) \
	    --api-addr http://127.0.0.1:$(DEV_API_PORT) \
	    --net-allow-cidrs=$(DEV_NET_ALLOW_CIDRS) ) & \
	worker_pid=$$!; \
	healthy=0; \
	for i in $$(seq 1 240); do \
	  if ! kill -0 $$worker_pid 2>/dev/null; then \
	    echo "make dev: worker exited before becoming healthy" >&2; exit 1; \
	  fi; \
	  if curl -sf http://127.0.0.1:$(DEV_WORKER_PORT)/healthz >/dev/null 2>&1; then \
	    healthy=1; break; \
	  fi; \
	  sleep 0.25; \
	done; \
	if [ $$healthy -ne 1 ]; then \
	  echo "make dev: worker did not answer /healthz within 60s" >&2; exit 1; \
	fi; \
	( bin/calcside-dev serve --dev --addr 127.0.0.1:$(DEV_API_PORT) \
	    --policy-dir policies/examples --secret-key $(CALCSIDE_SECRET_KEY) \
	    --dsn $(DEV_DSN) --ext-local-roots $(DEV_EXT_ROOTS) \
	    --net-allow-cidrs=$(DEV_NET_ALLOW_CIDRS) \
	    --workers $(DEV_WORKER_ID)=127.0.0.1:$(DEV_WORKER_PORT) \
	    --worker-key $(DEV_WORKER_KEY) ) & \
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
	  --dsn $(DEV_DSN) --ext-local-roots $(DEV_EXT_ROOTS) \
	  --net-allow-cidrs=$(DEV_NET_ALLOW_CIDRS)

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
