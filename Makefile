.PHONY: build install test vet fmt clean check help

BINARY := vpsdeploy
CMD := ./cmd/vpsdeploy
BUILD_DIR := bin
INSTALL_DIR ?= $(HOME)/bin

help:
	@echo "Available targets:"
	@echo "  build    - Build the vpsdeploy binary to $(BUILD_DIR)/"
	@echo "  install  - Build and copy to $(INSTALL_DIR)/"
	@echo "  test     - Run all unit tests"
	@echo "  vet      - Run go vet"
	@echo "  fmt      - Format Go source files"
	@echo "  check    - Contributor gate: fmt check + vet + race tests"
	@echo "  clean    - Remove build artifacts"

build:
	@mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(BINARY) $(CMD)

install: build
	@mkdir -p $(INSTALL_DIR)
	cp $(BUILD_DIR)/$(BINARY) $(INSTALL_DIR)/$(BINARY)
	@echo "Installed $(INSTALL_DIR)/$(BINARY)"

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
