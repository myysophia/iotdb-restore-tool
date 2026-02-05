.PHONY: build test clean run fmt vet lint docker docker-push

APP_NAME=iotdb-restore
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT=$(shell git rev-parse --short HEAD 2>/dev/null || echo 'none')
BUILD_DATE=$(shell date -u '+%Y-%m-%d %H:%M:%S')
LDFLAGS=-ldflags "-X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X 'main.Date=$(BUILD_DATE)'"

build:
	@echo "Building $(APP_NAME)..."
	@go build $(LDFLAGS) -o bin/$(APP_NAME) ./cmd/$(APP_NAME)

test:
	@echo "Running tests..."
	@go test -v -race -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html

clean:
	@echo "Cleaning..."
	@rm -rf bin/
	@rm -f coverage.out coverage.html

run:
	@go run ./cmd/$(APP_NAME) --config configs/config.yaml

fmt:
	@echo "Formatting code..."
	@go fmt ./...

vet:
	@echo "Vetting code..."
	@go vet ./...

lint:
	@echo "Linting code..."
	@golangci-lint run ./... || true

deps:
	@echo "Downloading dependencies..."
	@go mod download
	@go mod tidy

docker:
	@echo "Building Docker image..."
	@docker build -t $(APP_NAME):$(VERSION) -f deployments/Dockerfile .
	@docker tag $(APP_NAME):$(VERSION) $(APP_NAME):latest

docker-push:
	@echo "Pushing Docker image..."
	@docker push $(APP_NAME):$(VERSION)

help:
	@echo "Available targets:"
	@echo "  build       - Build the application"
	@echo "  test        - Run tests with coverage"
	@echo "  clean       - Clean build artifacts"
	@echo "  run         - Run the application"
	@echo "  fmt         - Format code"
	@echo "  vet         - Run go vet"
	@echo "  lint        - Run linter"
	@echo "  deps        - Download and tidy dependencies"
	@echo "  docker      - Build Docker image"
	@echo "  docker-push - Push Docker image"
	@echo "  help        - Show this help message"
