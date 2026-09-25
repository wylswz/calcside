BINARY := bin/calcside

.PHONY: build test lint run-dev tidy

build:
	go build -o $(BINARY) ./cmd/calcside

test:
	go test -race ./...

lint:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

run-dev:
	go run ./cmd/calcside serve --dev-login --policy-dir policies/examples

tidy:
	go mod tidy
