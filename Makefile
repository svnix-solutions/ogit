# Makefile for Git Server S3

.PHONY: help test test-unit test-integration test-coverage build run clean lint fmt vet deps

# Variables
BINARY_NAME=git-server-s3
BUILD_DIR=tmp
CONFIG_FILE=config.yaml

# Help
help: ## Display this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

# Install dependencies
deps: ## Install dependencies
	go mod download
	go mod tidy

# Unit tests
test-unit: ## Run unit tests
	go test -v -race -timeout 30s ./pkg/... ./internal/...

# Tests with coverage
test-coverage: ## Run tests with coverage
	go test -v -race -coverprofile=coverage.out -covermode=atomic ./pkg/... ./internal/...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

# Integration tests (MinIO via testcontainers)
test-integration: ## Run integration tests against MinIO (requires Docker)
	@if ! docker info >/dev/null 2>&1; then \
		echo "❌ Docker is not running. The integration tests start a MinIO container."; \
		exit 1; \
	fi
	go test -v -tags=integration -timeout 5m ./...

# All tests
test: test-unit ## Run all tests (unit tests only by default)

# Tests with more verbosity
test-verbose: ## Run tests with more details
	go test -v -race -coverprofile=coverage.out ./pkg/... ./internal/... -args -test.v

# Code formatting
fmt: ## Format code
	go fmt ./...

# Static checks
vet: ## Check code with go vet
	go vet ./...

# Linting (requires golangci-lint)
lint: ## Lint code (requires golangci-lint)
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "⚠️  golangci-lint not installed. Installation:"; \
		echo "go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
	fi

# Build
build: ## Build the server
	@mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/cmd.go

# Build with version information
build-release: ## Build for release
	@mkdir -p $(BUILD_DIR)
	go build -ldflags "-X main.version=$(shell git describe --tags --always --dirty)" \
		-o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/cmd.go

# Run the server
run: build ## Build and run the server
	./$(BUILD_DIR)/$(BINARY_NAME) server --config $(CONFIG_FILE)

# Run in development mode with auto-reload (requires air)
dev: ## Run in development mode (requires air)
	@if command -v air >/dev/null 2>&1; then \
		air; \
	else \
		echo "⚠️  air not installed. Installation:"; \
		echo "go install github.com/cosmtrek/air@latest"; \
		echo "Or use: make run"; \
	fi

# Cleanup
clean: ## Clean generated files
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html
	go clean -cache -testcache

# Performance tests
bench: ## Run benchmarks
	go test -bench=. -benchmem -run=^$$ ./pkg/... ./internal/...

# Race condition tests
race: ## Run tests with race condition detection
	go test -race ./pkg/... ./internal/...

# Security check (requires gosec)
security: ## Check code security (requires gosec)
	@if command -v gosec >/dev/null 2>&1; then \
		gosec ./...; \
	else \
		echo "⚠️  gosec not installed. Installation:"; \
		echo "go install github.com/securecodewarrior/gosec/v2/cmd/gosec@latest"; \
	fi

# Update dependencies
update-deps: ## Update dependencies
	go get -u ./...
	go mod tidy

# Complete check (CI)
ci: fmt vet test-unit ## CI/CD checks

# Install development tools
install-tools: ## Install development tools
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	go install github.com/cosmtrek/air@latest
	go install github.com/securecodewarrior/gosec/v2/cmd/gosec@latest

# Tests with different log levels
test-debug: ## Run tests with debug logs
	ZEROLOG_LEVEL=debug go test -v ./pkg/... ./internal/...

# Tests on specific files
test-s3: ## Test only the S3 package
	go test -v ./pkg/storage/s3/...

test-api: ## Test only the API
	go test -v ./internal/api/...

# Test statistics
test-stats: ## Test statistics
	@echo "📊 Test statistics:"
	@find . -name "*_test.go" -not -path "./vendor/*" | wc -l | xargs echo "Test files:"
	@grep -r "func Test" --include="*_test.go" . | wc -l | xargs echo "Test functions:"
	@grep -r "func Benchmark" --include="*_test.go" . | wc -l | xargs echo "Benchmarks:"
