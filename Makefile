# Bittech developer tasks. Run `make help` for the list.
#
# Local dev assumes Postgres + Redis are reachable (either via
# `make services` with Docker, or a locally running instance).

DATABASE_URL ?= postgres://bittech:bittech@127.0.0.1:5432/bittech
COMPOSE      := docker compose -f infra/docker-compose.yml
PSQL         := psql "$(DATABASE_URL)"

.PHONY: help fmt vet test build run migrate services services-down demo down \
        web-install web-dev web-build tidy

help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

fmt: ## Format Go code
	gofmt -w .

vet: ## Run go vet
	go vet ./...

test: ## Run the full Go test suite (needs Postgres + Redis)
	go test ./... -count=1

build: ## Build the backend binary into bin/
	CGO_ENABLED=0 go build -o bin/exchange ./services/exchange/cmd/exchange

tidy: ## Tidy go.mod/go.sum
	go mod tidy

migrate: ## Apply all SQL migrations to $$DATABASE_URL (in order)
	@for f in infra/db/*.sql; do echo "applying $$f"; $(PSQL) -q -f $$f; done

run: build ## Run the backend locally (DEV_FAUCET on)
	DATABASE_URL="$(DATABASE_URL)" DEV_FAUCET=1 ALLOWED_WS_ORIGINS=localhost:3000 \
		APIKEY_ENC_KEY=$$(openssl rand -hex 32) ./bin/exchange

services: ## Start backing services (Postgres, Redis, Redpanda) via Docker
	$(COMPOSE) up -d postgres redis redpanda

services-down: ## Stop backing services
	$(COMPOSE) stop postgres redis redpanda

web-install: ## Install frontend dependencies
	cd web && npm install

web-dev: ## Run the frontend dev server (proxies to :8080)
	cd web && API_BASE=http://localhost:8080 npm run dev

web-build: ## Production-build the frontend
	cd web && npm run build

demo: ## Build and run the WHOLE stack (backend + web + deps) in Docker
	$(COMPOSE) up --build

down: ## Tear down the whole stack (keeps volumes)
	$(COMPOSE) down
