BINARIES := bin/calcside bin/csctl

.PHONY: build test lint web dev run-dev tidy

build: web $(BINARIES)

$(BINARIES): $(shell find cmd internal -name '*.go')
	go build -o bin/calcside ./cmd/calcside
	go build -o bin/csctl ./cmd/csctl

web:
	pnpm -C web install --frozen-lockfile
	pnpm -C web build

test:
	go test -race ./...

lint:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	pnpm -C web typecheck
	pnpm -C web lint

# DEV ONLY — fixed throwaway key so `make dev` enables the secrets vault.
CALCSIDE_SECRET_KEY ?= MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=

dev run-dev:
	go run ./cmd/calcside serve --dev-login --policy-dir policies/examples --secret-key $(CALCSIDE_SECRET_KEY)

tidy:
	go mod tidy
