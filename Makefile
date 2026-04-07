.PHONY: build test lint proto clean

# Build all Go binaries
build:
	go build ./...

# Run all tests
test:
	go test ./...

# Run integration tests (requires PG/Redis)
test-integration:
	go test -tags=integration ./test/...

# Run linter
lint:
	golangci-lint run

# Generate protobuf/gRPC code
proto:
	buf generate

# Clean build artifacts
clean:
	rm -rf bin/ dist/ api/proto/gen/
	go clean ./...
