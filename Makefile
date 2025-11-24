.PHONY: help build run test-integration clean deps docker-up docker-down docker-restart setup dev-test

# Variables
BINARY_NAME=qcom-server
BINARY_PATH=./bin/$(BINARY_NAME)
MAIN_PATH=./cmd/server
DOCKER_COMPOSE=docker-compose
TEST_SCRIPT=./scripts/integration-test.sh

# Default target
.DEFAULT_GOAL := help

help: ## Show this help message
	@echo "Available commands:"
	@echo ""
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'
	@echo ""

deps: ## Download Go dependencies
	@echo "Downloading dependencies..."
	@go mod download
	@go mod tidy

build: deps ## Build the application
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p bin
	@go build -o $(BINARY_PATH) $(MAIN_PATH)
	@echo "Build complete: $(BINARY_PATH)"

run: ## Run the application (requires dependencies to be running)
	@echo "Starting $(BINARY_NAME)..."
	@if [ ! -f $(BINARY_PATH) ]; then \
		echo "Binary not found. Building first..."; \
		$(MAKE) build; \
	fi
	@$(BINARY_PATH)

docker-up: ## Start Docker container (DynamoDB)
	@echo "Starting Docker containers..."
	@$(DOCKER_COMPOSE) up -d dynamodb-local
	@echo "Waiting for services to be ready..."
	@sleep 5
	@echo "Docker containers started"

docker-down: ## Stop Docker containers
	@echo "Stopping Docker containers..."
	@$(DOCKER_COMPOSE) down
	@echo "Docker containers stopped"

docker-restart: docker-down docker-up ## Restart Docker containers

setup: docker-up ## Setup development environment (start containers and create table)
	@echo "Setting up development environment..."
	@if [ -f ./scripts/create-table.sh ]; then \
		chmod +x ./scripts/create-table.sh; \
		./scripts/create-table.sh || echo "Table may already exist"; \
	fi
	@echo "Setup complete"

test-integration: build docker-up ## Build, start dependencies, and run integration tests
	@echo "Running integration tests..."
	@if [ ! -f $(TEST_SCRIPT) ]; then \
		echo "Error: Integration test script not found at $(TEST_SCRIPT)"; \
		exit 1; \
	fi
	@chmod +x $(TEST_SCRIPT)
	@$(TEST_SCRIPT)

clean: ## Clean build artifacts
	@echo "Cleaning build artifacts..."
	@rm -rf bin/
	@rm -f /tmp/$(BINARY_NAME)
	@rm -f /tmp/server.log
	@echo "Clean complete"

clean-all: clean docker-down ## Clean everything including Docker containers
	@echo "Cleaning everything..."

dev: setup run ## Start development environment and run server

dev-test: ## Start server with local containers for manual API testing
	@echo "Setting up development test environment..."
	@echo ""
	@echo "Starting Docker containers..."
	@docker stop qcom-dynamodb 2>/dev/null || true
	@docker rm qcom-dynamodb 2>/dev/null || true
	@docker run -d --name qcom-dynamodb -p 8000:8000 \
		-e AWS_ACCESS_KEY_ID=dummy \
		-e AWS_SECRET_ACCESS_KEY=dummy \
		-e AWS_DEFAULT_REGION=us-east-1 \
		amazon/dynamodb-local:2.0.0 \
		-jar DynamoDBLocal.jar -sharedDb -inMemory
	@echo "Waiting for services to be ready..."
	@sleep 3
	@echo ""
	@echo "Creating DynamoDB table..."
	@if [ -f ./scripts/create-table.sh ]; then \
		chmod +x ./scripts/create-table.sh; \
		DYNAMODB_TABLE_NAME=QComTable ./scripts/create-table.sh > /dev/null 2>&1 || \
		(aws dynamodb delete-table --table-name QComTable --endpoint-url http://localhost:8000 --region us-east-1 > /dev/null 2>&1; sleep 2; DYNAMODB_TABLE_NAME=QComTable ./scripts/create-table.sh > /dev/null 2>&1); \
	fi
	@echo ""
	@echo "=========================================="
	@echo "Development Test Environment Ready"
	@echo "=========================================="
	@echo ""
	@echo "Dependencies:"
	@echo "  - DynamoDB: http://localhost:8000"
	@echo "  - Redis: localhost:6379"
	@echo ""
	@echo "Environment variables set:"
	@echo "  - DYNAMODB_ENDPOINT=http://localhost:8000"
	@echo "  - DYNAMODB_REGION=us-east-1"
	@echo "  - DYNAMODB_TABLE_NAME=QComTable"
	@echo "  - REDIS_ENDPOINT=localhost:6379"
	@echo "  - PORT=8080"
	@echo ""
	@echo "Server will start with these settings."
	@echo "Press Ctrl+C to stop the server."
	@echo ""
	@echo "=========================================="
	@echo ""
	@if [ ! -f $(BINARY_PATH) ]; then \
		echo "Building application..."; \
		$(MAKE) build; \
	fi
	@echo "Starting server..."
	@echo ""
	@JWT_SECRET_KEY=$$(openssl rand -base64 32) \
	DYNAMODB_ENDPOINT=http://localhost:8000 \
	DYNAMODB_REGION=us-east-1 \
	DYNAMODB_TABLE_NAME=QComTable \
	REDIS_ENDPOINT=localhost:6379 \
	REDIS_PASSWORD= \
	REDIS_DB=0 \
	PORT=8080 \
	OTP_LENGTH=6 \
	OTP_EXPIRY=10m \
	OTP_MAX_ATTEMPTS=5 \
	$(BINARY_PATH)

test: ## Run unit tests
	@echo "Running unit tests..."
	@go test -v ./...

test-coverage: ## Run tests with coverage
	@echo "Running tests with coverage..."
	@go test -v -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

lint: ## Run linter
	@echo "Running linter..."
	@golangci-lint run ./... || echo "golangci-lint not installed. Install with: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"

fmt: ## Format code
	@echo "Formatting code..."
	@go fmt ./...

vet: ## Run go vet
	@echo "Running go vet..."
	@go vet ./...

check: fmt vet lint ## Run all checks (format, vet, lint)

.DEFAULT_GOAL := help

