.PHONY: build clean run test install

# Binary name
BINARY=traceroute-scanner

# Build the project
build:
	go build -o $(BINARY)

# Build with optimizations
build-release:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o $(BINARY)

# Clean build artifacts
clean:
	go clean
	rm -f $(BINARY)
	rm -f *.jsonl

# Run with example (requires sudo)
run: build
	sudo ./$(BINARY) -range 8.8.8.8

# Run tests (if any are added later)
test:
	go test -v ./...

# Install dependencies
deps:
	go mod download
	go mod tidy

# Format code
fmt:
	go fmt ./...

# Run linter (requires golangci-lint)
lint:
	golangci-lint run

# Show help
help:
	@echo "Available targets:"
	@echo "  build         - Build the binary"
	@echo "  build-release - Build optimized binary"
	@echo "  clean         - Remove build artifacts"
	@echo "  run           - Build and run with example (requires sudo)"
	@echo "  test          - Run tests"
	@echo "  deps          - Download dependencies"
	@echo "  fmt           - Format code"
	@echo "  lint          - Run linter"
	@echo "  help          - Show this help"
