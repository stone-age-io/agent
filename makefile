# Windows Agent Makefile

# Version can be overridden: make build VERSION=1.2.3
VERSION ?= 1.0.0

# Binary names
BINARY_NAME := win-agent.exe
BUILD_DIR := build

# Go build flags
LDFLAGS := -X 'main.version=$(VERSION)' -s -w
GOFLAGS := -trimpath

.PHONY: all
all: clean test build

# Build for current platform (development)
.PHONY: build
build:
	@echo "Building $(BINARY_NAME) version $(VERSION) for current platform..."
	go build -ldflags="$(LDFLAGS)" -o $(BINARY_NAME) ./cmd/win-agent
	@echo "Build complete: $(BINARY_NAME)"

# Build for Windows AMD64 (release)
.PHONY: build-release
build-release:
	@echo "Building $(BINARY_NAME) version $(VERSION) for Windows AMD64..."
	@mkdir -p $(BUILD_DIR)
	GOOS=windows GOARCH=amd64 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/win-agent
	@echo "Release build complete: $(BUILD_DIR)/$(BINARY_NAME)"
	@echo "Size: $$(du -h $(BUILD_DIR)/$(BINARY_NAME) | cut -f1)"

# Run tests
.PHONY: test
test:
	@echo "Running tests..."
	go test -v -race -coverprofile=coverage.out ./...
	@echo "Coverage report: coverage.out"

# Run tests with coverage report
.PHONY: test-coverage
test-coverage: test
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# Run linter
.PHONY: lint
lint:
	@echo "Running linter..."
	golangci-lint run

# Format code
.PHONY: fmt
fmt:
	@echo "Formatting code..."
	go fmt ./...
	goimports -w .

# Download dependencies
.PHONY: deps
deps:
	@echo "Downloading dependencies..."
	go mod download
	go mod tidy

# Verify dependencies
.PHONY: verify
verify:
	@echo "Verifying dependencies..."
	go mod verify

# Clean build artifacts
.PHONY: clean
clean:
	@echo "Cleaning build artifacts..."
	rm -f $(BINARY_NAME)
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html
	@echo "Clean complete"

# Install development tools
.PHONY: install-tools
install-tools:
	@echo "Installing development tools..."
	go install golang.org/x/tools/cmd/goimports@latest
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	@echo "Tools installed"

# Generate example config
.PHONY: example-config
example-config:
	@echo "Example config is available at: config.yaml.example"

# Show version
.PHONY: version
version:
	@echo "Version: $(VERSION)"

# Help
.PHONY: help
help:
	@echo "Windows Agent Build System"
	@echo ""
	@echo "Usage: make [target] [VERSION=x.y.z]"
	@echo ""
	@echo "Targets:"
	@echo "  build              Build for current platform"
	@echo "  build-release      Build for Windows AMD64 (production)"
	@echo "  test               Run tests"
	@echo "  test-coverage      Run tests with coverage report"
	@echo "  lint               Run linter"
	@echo "  fmt                Format code"
	@echo "  deps               Download dependencies"
	@echo "  verify             Verify dependencies"
	@echo "  clean              Clean build artifacts"
	@echo "  install-tools      Install development tools"
	@echo "  version            Show current version"
	@echo "  help               Show this help message"
	@echo ""
	@echo "Examples:"
	@echo "  make build"
	@echo "  make build-release VERSION=1.2.3"
	@echo "  make test"
