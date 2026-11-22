# Common tasks. Run `make help` to list them.
BIN := bin/hub
PKG := ./...

.PHONY: help run build test test-race cover sandbox-test lint fmt vet tidy db-up db-down docker \
	up down clean logs ps demo demo-local

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-13s %s\n", $$1, $$2}'

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

sandbox-test: ## Check adapters against real vendor sandboxes (keys from .env, see docs/providers)
	@set -a; [ -f .env ] && . ./.env; set +a; go test -tags sandbox -run TestSandbox -count=1 -v ./internal/gateway/...

lint: ## Run golangci-lint
	golangci-lint run

fmt: ## Format code
	gofmt -w .

vet: ## Run go vet
	go vet $(PKG)

tidy: ## Tidy go.mod
	go mod tidy

db-up: ## Start only PostgreSQL in Docker (for `make run`)
	docker compose up -d --wait postgres

db-down: ## Stop the Docker stack
	docker compose down

docker: ## Build the production image
	docker build --target hub -t payment-gateway-hub .

# Local stack: hub + Postgres + mock Midtrans + webhook sink
up: ## Build and start the local stack, wait until healthy
	docker compose up -d --build --wait

down: ## Stop the local stack (keeps data)
	docker compose down

clean: ## Stop the local stack and delete its data
	docker compose down -v

logs: ## Follow logs of the local stack
	docker compose logs -f hub mock-midtrans webhook-sink

ps: ## Show the local stack's containers
	docker compose ps

demo: ## Run the end-to-end demo inside Docker
	docker compose run --rm --build demo

demo-local: ## Run the end-to-end demo from the host (needs Go)
	go run ./dev/demo
