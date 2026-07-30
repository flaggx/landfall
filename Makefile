.PHONY: build install test vet fmt clean help

BINARY := vpsdeploy
CMD := ./cmd/vpsdeploy
BUILD_DIR := bin
INSTALL_DIR ?= $(HOME)/bin

help:
	@echo "Available targets:"
	@echo "  build    - Build the vpsdeploy binary to $(BUILD_DIR)/"
	@echo "  install  - Build and copy to $(INSTALL_DIR)/"
	@echo "  test     - Run all tests"
	@echo "  vet      - Run go vet"
	@echo "  fmt      - Format Go source files"
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

clean:
	rm -rf $(BUILD_DIR)/$(BINARY)
