.PHONY: build install test vet fmt clean check help

BINARY := landfall
CMD := ./cmd/landfall
BUILD_DIR := bin
INSTALL_DIR ?= $(HOME)/bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X github.com/flaggx/landfall/internal/cli.Version=$(VERSION)"

help:
	@echo "Available targets:"
	@echo "  build    - Build the landfall binary to $(BUILD_DIR)/"
	@echo "  install  - Build and copy to $(INSTALL_DIR)/"
	@echo "  test     - Run all unit tests"
	@echo "  vet      - Run go vet"
	@echo "  fmt      - Format Go source files"
	@echo "  check    - Contributor gate: fmt check + vet + race tests"
	@echo "  clean    - Remove build artifacts"

build:
	@mkdir -p $(BUILD_DIR)
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY) $(CMD)

install: build
	@mkdir -p $(INSTALL_DIR)
	cp $(BUILD_DIR)/$(BINARY) $(INSTALL_DIR)/$(BINARY)
	@echo "Installed $(INSTALL_DIR)/$(BINARY) ($(VERSION))"

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed on:" && gofmt -l . && exit 1)
	go vet ./...
	go test -race ./...

clean:
	rm -rf $(BUILD_DIR)/$(BINARY)
