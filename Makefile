.PHONY: build run test clean tidy migrate docker-up docker-down

# Binary output name
BINARY=server
BUILD_DIR=./bin

# Build the server binary
build:
	go build -ldflags="-s -w" -o $(BUILD_DIR)/$(BINARY) ./cmd/server/

# Run the server locally
run:
	go run ./cmd/server/

# Run tests
test:
	go test -v -race ./...

# Clean build artifacts
clean:
	rm -rf $(BUILD_DIR)
	rm -rf logs/

# Download and tidy dependencies
tidy:
	go mod tidy

# Run database migration (via starting the server which auto-migrates)
migrate: build
	$(BUILD_DIR)/$(BINARY)

# Start local MySQL + Redis via docker-compose
docker-up:
	docker compose up -d mysql redis

# Stop local services
docker-down:
	docker compose down

# Start all services (including go-server) via root docker-compose
docker-all:
	cd .. && docker compose up -d --build

# Format code
fmt:
	go fmt ./...

# Lint (requires golangci-lint)
lint:
	golangci-lint run ./...

# Build Docker image
docker-build:
	docker build -t tokenhub-server .
