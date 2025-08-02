# Justfile for PEVM-Go project

# Default recipe - run format, lint, test, and build
default: fmt lint test build

# Build the project
build:
    @echo "Building PEVM-Go..."
    go build -v ./...

# Run tests
test:
    @echo "Running tests..."
    go test -v ./...

# Run tests with race detection
test-race:
    @echo "Running tests with race detection..."
    go test -race -v ./...

# Run benchmarks
bench:
    @echo "Running benchmarks..."
    go test -bench=. -benchmem ./...

# Run storage benchmarks
bench-storage:
    @echo "Running storage benchmarks..."
    go test -bench=BenchmarkStorage -benchmem ./storage/

# Run scheduler benchmarks
bench-scheduler:
    @echo "Running scheduler benchmarks..."
    go test -bench=BenchmarkScheduler -benchmem ./scheduler/

# Run executor benchmarks
bench-executor:
    @echo "Running executor benchmarks..."
    go test -bench=BenchmarkExecutor -benchmem ./executor/

# Clean build artifacts
clean:
    @echo "Cleaning..."
    go clean -testcache
    go clean -cache
    rm -f cpu.prof mem.prof trace.out

# Lint the code
lint:
    @echo "Running linter..."
    @if ! command -v golangci-lint >/dev/null 2>&1; then \
        echo "Installing golangci-lint..."; \
        go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest; \
    fi
    golangci-lint run ./...

# Format code
fmt:
    @echo "Formatting code..."
    go fmt ./...
    @if ! command -v goimports >/dev/null 2>&1; then \
        echo "Installing goimports..."; \
        go install golang.org/x/tools/cmd/goimports@latest; \
    fi
    goimports -w .

# Install dependencies
deps:
    @echo "Installing dependencies..."
    go mod download
    go mod verify

# Check for vulnerabilities
check:
    @echo "Checking for vulnerabilities..."
    @if ! command -v govulncheck >/dev/null 2>&1; then \
        echo "Installing govulncheck..."; \
        go install golang.org/x/vuln/cmd/govulncheck@latest; \
    fi
    govulncheck ./...

# Install the project
install:
    @echo "Installing PEVM-Go..."
    go install ./...

# Run basic example
run-example:
    @echo "Running basic example..."
    cd examples/basic && go run main.go

# Run CPU profiling
profile-cpu:
    @echo "Running CPU profile..."
    go test -cpuprofile=cpu.prof -bench=. ./...
    go tool pprof cpu.prof

# Run memory profiling
profile-mem:
    @echo "Running memory profile..."
    go test -memprofile=mem.prof -bench=. ./...
    go tool pprof mem.prof

# Run execution trace
profile-trace:
    @echo "Running execution trace..."
    go test -trace=trace.out -bench=. ./...
    go tool trace trace.out

# Generate documentation server
docs:
    @echo "Generating documentation..."
    @if ! command -v godoc >/dev/null 2>&1; then \
        echo "Installing godoc..."; \
        go install golang.org/x/tools/cmd/godoc@latest; \
    fi
    godoc -http=:6060

# Run coverage analysis
coverage:
    @echo "Running coverage analysis..."
    go test -coverprofile=coverage.out ./...
    go tool cover -html=coverage.out -o coverage.html
    @echo "Coverage report generated: coverage.html"

# Generate storage package coverage
coverage-storage:
    @echo "Running storage coverage..."
    go test -coverprofile=coverage-storage.out ./storage/
    go tool cover -html=coverage-storage.out -o coverage-storage.html

# Generate scheduler package coverage
coverage-scheduler:
    @echo "Running scheduler coverage..."
    go test -coverprofile=coverage-scheduler.out ./scheduler/
    go tool cover -html=coverage-scheduler.out -o coverage-scheduler.html

# Generate executor package coverage
coverage-executor:
    @echo "Running executor coverage..."
    go test -coverprofile=coverage-executor.out ./executor/
    go tool cover -html=coverage-executor.out -o coverage-executor.html

# Run all CI checks
ci: fmt lint test-race bench coverage check

# Development workflow
dev: deps fmt lint test

# Production build with optimizations
release: clean deps fmt lint test-race bench
    @echo "Building release version..."
    CGO_ENABLED=0 go build -ldflags="-s -w" -v ./...

# Build Docker image
docker-build:
    @echo "Building Docker image..."
    docker build -t pevm-go:latest .

# Tidy go modules
mod-tidy:
    @echo "Tidying go modules..."
    go mod tidy

# Verify go modules
mod-verify:
    @echo "Verifying go modules..."
    go mod verify

# Vendor dependencies
mod-vendor:
    @echo "Vendoring dependencies..."
    go mod vendor

# Run security analysis
security:
    @echo "Running security checks..."
    @if ! command -v gosec >/dev/null 2>&1; then \
        echo "Installing gosec..."; \
        go install github.com/securecodewarrior/gosec/v2/cmd/gosec@latest; \
    fi
    gosec ./...

# Analyze code complexity
complexity:
    @echo "Analyzing code complexity..."
    @if ! command -v gocyclo >/dev/null 2>&1; then \
        echo "Installing gocyclo..."; \
        go install github.com/fzipp/gocyclo/cmd/gocyclo@latest; \
    fi
    gocyclo -over 15 .

# Count lines of code
lines:
    @echo "Counting lines of code..."
    @if command -v cloc >/dev/null 2>&1; then \
        cloc --exclude-dir=vendor,.git .; \
    else \
        echo "Install cloc for line counting"; \
        find . -name "*.go" -not -path "./vendor/*" -not -path "./.git/*" | xargs wc -l; \
    fi

# Run stress tests
stress-test:
    @echo "Running stress tests..."
    go test -v -count=100 -short ./...

# Run load tests
load-test:
    @echo "Running load tests..."
    cd examples/basic && go run main.go

# Setup git hooks
setup-hooks:
    @echo "Setting up git hooks..."
    @if [ -f scripts/pre-commit ]; then \
        cp scripts/pre-commit .git/hooks/; \
        chmod +x .git/hooks/pre-commit; \
    else \
        echo "pre-commit script not found in scripts/"; \
    fi

# Show version and build info
version:
    @echo "Go version: $(go version)"
    @echo "Git commit: $(git rev-parse --short HEAD 2>/dev/null || echo 'not a git repository')"
    @echo "Build time: $(date -u '+%Y-%m-%d %H:%M:%S UTC')"

# Show available recipes
help:
    @echo "Available recipes:"
    @echo "  default        - Run fmt, lint, test, and build"
    @echo "  build          - Build the project"
    @echo "  test           - Run tests"
    @echo "  test-race      - Run tests with race detection"
    @echo "  bench          - Run all benchmarks"
    @echo "  bench-*        - Run specific package benchmarks"
    @echo "  clean          - Clean build artifacts"
    @echo "  lint           - Run linter"
    @echo "  fmt            - Format code"
    @echo "  deps           - Install dependencies"
    @echo "  check          - Check for vulnerabilities"
    @echo "  install        - Install the project"
    @echo "  run-example    - Run the basic example"
    @echo "  profile-*      - Run performance profiling"
    @echo "  docs           - Generate documentation server"
    @echo "  coverage       - Generate coverage report"
    @echo "  coverage-*     - Generate package-specific coverage"
    @echo "  ci             - Run all CI checks"
    @echo "  dev            - Development workflow"
    @echo "  release        - Production build"
    @echo "  docker-build   - Build Docker image"
    @echo "  mod-*          - Go module operations"
    @echo "  security       - Run security analysis"
    @echo "  complexity     - Analyze code complexity"
    @echo "  lines          - Count lines of code"
    @echo "  stress-test    - Run stress tests"
    @echo "  load-test      - Run load tests"
    @echo "  setup-hooks    - Setup git hooks"
    @echo "  version        - Show version and build info"
    @echo "  help           - Show this help"

# Watch for changes and run tests (requires just watch feature)
watch:
    @echo "Watching for changes..."
    just --watch test

# Quick development check
quick: fmt lint
    @echo "Quick development checks completed"

# All quality checks
quality: lint security complexity coverage
    @echo "All quality checks completed"
