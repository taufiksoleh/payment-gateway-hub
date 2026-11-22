# Common tasks. Run `make help` to list them.
BIN := bin/hub
PKG := ./...

.PHONY: help run build test test-race cover lint fmt vet tidy db-up db-down docker

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

run: ## Run the hub (loads .env if present)
	@set -a; [ -f .env ] && . ./.env; set +a; go run ./cmd/hub

build: ## Build the binary into bin/
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/hub

test: ## Run unit tests
	go test $(PKG)

test-race: ## Run tests with the race detector
	go test -race $(PKG)

cover: ## Run tests with a coverage report
	go test -coverprofile=coverage.out $(PKG) && go tool cover -func=coverage.out | tail -1

lint: ## Run golangci-lint
	golangci-lint run

fmt: ## Format code
	gofmt -w .

vet: ## Run go vet
	go vet $(PKG)

tidy: ## Tidy go.mod
	go mod tidy

db-up: ## Start PostgreSQL in Docker
	docker compose up -d postgres

db-down: ## Stop PostgreSQL
	docker compose down

docker: ## Build the Docker image
	docker build -t payment-gateway-hub .
