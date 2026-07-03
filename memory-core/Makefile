.PHONY: build run test clean migrate lint deps build-all

# Build the memory-core binary
build:
	go build -o bin/memory-core ./cmd/server

# Run the memory-core service
run:
	go run ./cmd/server

# Run all tests
test:
	go test ./... -v -race -cover

# Run tests with coverage report
test-cover:
	go test ./... -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html

# Run linter
lint:
	golangci-lint run ./...

# Install dependencies
deps:
	go mod tidy
	go mod download

# Run database migration (requires psql)
migrate:
	psql -U $(DB_USER) -h $(DB_HOST) -d $(DB_NAME) -f migration/001_initial.sql

# Clean build artifacts
clean:
	rm -rf bin/

# Start with Docker Compose (PostgreSQL + Redis)
docker-up:
	docker-compose up -d

# Stop Docker services
docker-down:
	docker-compose down

# Generate proto stubs (if using gRPC)
proto:
	protoc --go_out=. --go-grpc_out=. proto/*.proto

# Development: run with hot reload (requires air)
dev:
	air

# Build for multiple platforms (static, stripped binaries)
build-all:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/memory-core-windows-amd64.exe ./cmd/server/
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/memory-core-darwin-amd64 ./cmd/server/
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o bin/memory-core-darwin-arm64 ./cmd/server/
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o bin/memory-core-android-arm64 ./cmd/server/
	@echo "Binaries built in bin/"
	@ls -lh bin/
