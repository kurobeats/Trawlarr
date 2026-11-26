BINARY  := trawlarr
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all ui build test test-short cover lint run dev docker clean

all: build

## ui: rebuild the embedded admin interface
ui:
	cd ui && npm install --silent && npm run build

## build: compile a single binary with the UI embedded
build: ui
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/trawlarr

## test: full suite with the race detector and no cached results
test:
	go test -race -count=1 ./...

## test-short: skip the race detector for a quick loop
test-short:
	go test -count=1 ./...

## cover: coverage summary plus an HTML report
cover:
	go test -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1
	go tool cover -html=coverage.out -o coverage.html

## lint: vet plus the TypeScript compiler
lint:
	go vet ./...
	cd ui && npx tsc --noEmit

## run: start the server with config.json in the repository root
run:
	go run ./cmd/trawlarr -config config.json

## dev: run the API and the Vite dev server together
dev:
	@echo "API on :3333, UI on :5173 (proxying /api)"
	go run ./cmd/trawlarr -config config.json & cd ui && npm run dev

docker:
	docker build -t trawlarr:$(VERSION) .

clean:
	rm -rf bin ui/node_modules internal/web/dist/assets
