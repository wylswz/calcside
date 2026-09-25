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

dev run-dev:
	go run ./cmd/calcside serve --dev-login --policy-dir policies/examples

tidy:
	go mod tidy
