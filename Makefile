# Piccolo — build and development tasks.
#
# Usage: make [target]   (default: build)

BINARY  := piccolo
PKG     := ./cmd/piccolo
GO      ?= go

.DEFAULT_GOAL := build

## build: compile the piccolo binary
build:
	$(GO) build -o $(BINARY) $(PKG)

## test: run all unit tests
test:
	$(GO) test ./...

## test-race: run tests with the race detector
test-race:
	$(GO) test -race ./...

## cover: run tests and print per-package coverage
cover:
	$(GO) test -cover ./...

## vet: run go vet
vet:
	$(GO) vet ./...

## fmt: format all Go files
fmt:
	$(GO) fmt ./...

## fmt-check: fail if any file is not gofmt-clean
fmt-check:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt-clean:"; echo "$$unformatted"; exit 1; \
	fi

## tidy: tidy go.mod / go.sum
tidy:
	$(GO) tidy

## lint: fmt-check + vet (CI gate)
lint: fmt-check vet

## check: the full local gate — lint, build, test
check: lint build test

## install: install the binary into $$GOPATH/bin (or GOBIN)
install:
	$(GO) install $(PKG)

## clean: remove build artifacts
clean:
	rm -f $(BINARY)
	$(GO) clean

## smoke: build, then init + doctor in a throwaway workspace (needs Docker daemon)
smoke: build
	@dir=$$(mktemp -d); \
	echo "smoke workspace: $$dir"; \
	./$(BINARY) -D "$$dir" init --yes --name Piccolo --owner Smoke \
		--purpose personal --agents research --no-encrypt \
		--provider openai --model gpt-5.4 --api-key sk-smokekey123 && \
	./$(BINARY) -D "$$dir" doctor; \
	status=$$?; rm -rf "$$dir"; exit $$status

## help: list available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'

.PHONY: build test test-race cover vet fmt fmt-check tidy lint check install clean smoke help
