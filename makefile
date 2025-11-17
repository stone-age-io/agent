# Agent Makefile

# Version can be overridden: make build VERSION=1.2.3
VERSION ?= 1.0.0

# Binary names
BINARY_BASE := agent
BUILD_DIR := build

# Go build flags
LDFLAGS := -X 'main.version=$(VERSION)' -s -w
GOFLAGS := -trimpath

.PHONY: all
all: clean test build

# Build for current platform (development)
.PHONY: build
build:
	@echo "Building $(BINARY_BASE) version $(VERSION) for current platform..."
	go build -ldflags="$(LDFLAGS)" -o $(BINARY_BASE) ./cmd/agent
	@echo "Build complete: $(BINARY_BASE)"

# Build for all platforms (release)
.PHONY: build-all
build-all:
	@echo "Building $(BINARY_BASE) version $(VERSION) for all platforms..."
	@mkdir -p $(BUILD_DIR)
	
	# Linux AMD64
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" \
		-o $(BUILD_DIR)/agent-linux-amd64 ./cmd/agent
	
	# Linux ARM64
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" \
		-o $(BUILD_DIR)/agent-linux-arm64 ./cmd/agent
	
	# Windows AMD64
	GOOS=windows GOARCH=amd64 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" \
		-o $(BUILD_DIR)/agent-windows-amd64.exe ./cmd/agent
	
	# FreeBSD AMD64
	GOOS=freebsd GOARCH=amd64 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" \
		-o $(BUILD_DIR)/agent-freebsd-amd64 ./cmd/agent
	
	@echo "Multi-platform build complete:"
	@ls -lh $(BUILD_DIR)/

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
	rm -f $(BINARY_BASE) $(BINARY_BASE).exe
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

# Show version
.PHONY: version
version:
	@echo "Version: $(VERSION)"

# Help
.PHONY: help
help:
	@echo "Agent Build System"
	@echo ""
	@echo "Usage: make [target] [VERSION=x.y.z]"
	@echo ""
	@echo "Targets:"
	@echo "  build              Build for current platform"
	@echo "  build-all          Build for all platforms (Linux, Windows, FreeBSD)"
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
	@echo "  make build-all VERSION=1.2.3"
	@echo "  make test"
