# Root Makefile for go-ldparser

.PHONY: all build ldcli ldviewer install uninstall clean test sanitize coverage scan help bin-dir

GOOS ?= $(shell go env GOOS)
ifeq ($(GOOS),windows)
	EXE_EXT := .exe
else
	EXE_EXT :=
endif

BIN_DIR := bin

all: build

build: bin-dir ldcli ldviewer

# Build ldcli to bin/ folder — picked up by external tools via PATH or direct reference.
ldcli: bin-dir
	go build -o $(BIN_DIR)/ldcli$(EXE_EXT) ./cmd/ldcli
	@echo "Built $(BIN_DIR)/ldcli$(EXE_EXT)"

# Build ldviewer to bin/ folder
ldviewer: bin-dir
	go build -o $(BIN_DIR)/ldviewer$(EXE_EXT) ./cmd/ldviewer
	@echo "Built $(BIN_DIR)/ldviewer$(EXE_EXT)"

bin-dir:
	mkdir -p $(BIN_DIR)

test:
	go test -v .

coverage:
	go test -coverprofile=coverage.out .
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated at coverage.html"
	@go tool cover -func=coverage.out | tail -1

scan:
	gitleaks detect --no-git --config .gitleaks.toml --verbose

sanitize: bin-dir
	@echo "Sanitize target would require cmd/debug tool — skipping"

install: ldcli ldviewer
	go install ./cmd/ldcli
	go install ./cmd/ldviewer

uninstall:
	rm -f $(GOBIN)/ldcli.exe $(GOBIN)/ldcli

clean:
	rm -rf $(BIN_DIR) coverage.out coverage.html

help:
	@echo "Available targets:"
	@echo "  all       - Build all tools to bin/ folder (default)"
	@echo "  build     - Build all tools to bin/ folder"
	@echo "  ldcli     - Build ldcli only to bin/ folder"
	@echo "  ldviewer  - Build ldviewer only to bin/ folder"
	@echo "  test      - Run all tests"
	@echo "  coverage  - Generate HTML coverage report"
	@echo "  scan      - Scan all files for sensitive info (using gitleaks)"
	@echo "  sanitize  - Anonymize .ld and .ldx (strips personal info)"
	@echo "  install   - Install all tools as global binaries"
	@echo "  uninstall - Uninstall all tools from global binaries"
	@echo "  clean     - Clean build artifacts (bin/ + coverage files)"
