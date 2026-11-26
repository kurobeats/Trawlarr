BINARY  := trawlarr
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all ui build test lint run dev docker clean

all: build

## ui: rebuild the embedded admin interface
ui:
	cd ui && npm install --silent && npm run build

## build: compile a single binary with the UI embedded
build: ui
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/trawlarr

## test: run the Go test suite (includes the fuzz seed corpus)
test:
	go test ./...

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
