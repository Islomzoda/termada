BINARY := bin/termada
PKG := ./...

# Use a locally-installed Go if it is not on PATH.
GO ?= $(shell command -v go 2>/dev/null || echo $(HOME)/.local/go/bin/go)

BIN_DIR ?= $(HOME)/.local/bin

.PHONY: all build install test vet fmt race run clean

all: vet test build

build:
	$(GO) build -o $(BINARY) ./cmd/termada

install:
	mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/termada ./cmd/termada
	@echo "installed $(BIN_DIR)/termada"

test:
	$(GO) test $(PKG)

race:
	$(GO) test -race $(PKG)

vet:
	$(GO) vet $(PKG)

fmt:
	$(GO) fmt $(PKG)

# Run the MCP server over stdio (the working phase-1 path).
run: build
	$(BINARY) serve --stdio

clean:
	rm -rf bin dist
