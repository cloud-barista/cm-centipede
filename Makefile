# Makefile for CM-Centipede in Cloud-Barista.
#
# Covers the Go build for cmd/cm-centipede and the Docker Compose development
# stack that runs its dependencies — modeled on cloud-barista/cm-beetle's
# Makefile. CM-Centipede is not yet part of that stack: its service block in
# deployments/docker-compose/docker-compose.yaml is still commented out, so
# 'make build' / 'make run' drive it locally against the composed dependencies.

SHELL := /bin/bash

.PHONY: build run swagger up dev-ui down prepare-volumes compose compose-down \
	build-honeybee init init-openbao unseal logs status ps clean-db clean-all help

# ===== Go build =====

build: ## Build the cm-centipede binary into bin/
	@mkdir -p bin
	@go build -o bin/cm-centipede ./cmd/cm-centipede
	@echo "Built bin/cm-centipede"

run: build ## Build and run cm-centipede locally (reads conf/cm-centipede.yaml)
	@./bin/cm-centipede

# dummy/ vendors other Cloud-Barista repos and transx-ex is a nested module;
# scanning either makes swag fail on types it cannot resolve.
swagger: ## Regenerate the OpenAPI docs in pkg/api/rest/docs from the handler annotations
	@go run github.com/swaggo/swag/cmd/swag@v1.16.6 init \
		--generalInfo cmd/cm-centipede/main.go \
		--dir ./ \
		--exclude ./dummy,./testenv,./deployments,./transx-ex \
		--output pkg/api/rest/docs \
		--parseDependency --parseInternal
	@echo "Regenerated pkg/api/rest/docs"

up: compose ## Build and up services by docker compose

dev-ui: ## Run UI dev server locally with hot-reload (run 'make up' first to start backends)
	@if [ ! -d ui ]; then \
		echo "No ui/ directory yet."; \
		echo "Copy cloud-barista/cm-beetle's ui/ directory here as a starting point, then re-run 'make dev-ui'."; \
		exit 1; \
	fi
	@echo "Stopping containerised UI if running..."
	@docker stop cm-centipede-ui 2>/dev/null || true
	@echo "Installing UI dependencies (if needed)..."
	@cd ui && npm install
	@echo "Starting Next.js dev server with hot-reload at http://localhost:3000"
	@echo "  (reads ui/.env.local — backends expected on localhost ports)"
	@cd ui && CHOKIDAR_USEPOLLING=true WATCHPACK_POLLING=true TURBOPACK_ROOT=$$(pwd) npm run dev

down: compose-down ## Down services by docker compose

prepare-volumes: ## Create bind-mount directories with correct ownership
	@echo "Preparing container-volume directories in deployments/docker-compose/data/..."
	@mkdir -p \
		deployments/docker-compose/data/cb-tumblebug-container/meta_db \
		deployments/docker-compose/data/cb-tumblebug-container/log \
		deployments/docker-compose/data/cb-spider-container/meta_db \
		deployments/docker-compose/data/cb-spider-container/log \
		deployments/docker-compose/data/etcd/data \
		deployments/docker-compose/data/openbao-data \
		deployments/docker-compose/data/mc-terrarium-container/.terrarium \
		deployments/docker-compose/data/cm-beetle-container/db \
		deployments/docker-compose/data/cm-beetle-container/log \
		deployments/docker-compose/data/cm-damselfly-container/db \
		deployments/docker-compose/data/cm-honeybee-container \
		deployments/docker-compose/data/openbao-honeybee-container/data \
		2>/dev/null || \
	sudo mkdir -p \
		deployments/docker-compose/data/cb-tumblebug-container/meta_db \
		deployments/docker-compose/data/cb-tumblebug-container/log \
		deployments/docker-compose/data/cb-spider-container/meta_db \
		deployments/docker-compose/data/cb-spider-container/log \
		deployments/docker-compose/data/etcd/data \
		deployments/docker-compose/data/openbao-data \
		deployments/docker-compose/data/mc-terrarium-container/.terrarium \
		deployments/docker-compose/data/cm-beetle-container/db \
		deployments/docker-compose/data/cm-beetle-container/log \
		deployments/docker-compose/data/cm-damselfly-container/db \
		deployments/docker-compose/data/cm-honeybee-container \
		deployments/docker-compose/data/openbao-honeybee-container/data
	@# Fix ownership for mc-terrarium volume (container runs as appuser, uid 1000)
	@if [ "$$(stat -c '%u' deployments/docker-compose/data/mc-terrarium-container/.terrarium 2>/dev/null)" != "$$(id -u)" ]; then \
		echo "Fixing ownership of mc-terrarium volume..."; \
		sudo chown -R $$(id -u):$$(id -g) deployments/docker-compose/data/mc-terrarium-container/.terrarium; \
	fi
	@echo "Prepared!"

compose: prepare-volumes ## Build and up services by docker compose
	@echo "Starting OpenBao..."
	@cd deployments/docker-compose && docker compose up -d openbao
	@if [ ! -f deployments/docker-compose/.env ] || ! grep -q '^VAULT_TOKEN=.\+' deployments/docker-compose/.env 2>/dev/null; then \
		echo "VAULT_TOKEN not found — running first-time OpenBao initialization..."; \
		bash deployments/docker-compose/openbao/openbao-init.sh; \
	fi
	@$(MAKE) unseal
	@echo "Building and starting all services by docker compose..."
	@cd deployments/docker-compose && DOCKER_BUILDKIT=1 docker compose up --build

compose-down: ## Down services by docker compose
	@echo "Removing services by docker compose..."
	@cd deployments/docker-compose && docker compose down

build-honeybee: ## Rebuild the cm-honeybee image from the local sibling cm-honeybee checkout
	@echo "Building cm-honeybee from ../cm-honeybee/server (sibling of this repo)..."
	@cd deployments/docker-compose && DOCKER_BUILDKIT=1 docker compose build cm-honeybee

# ===== Initialization =====

init: ## Run initialization sequence (credential registration for OpenBao and Tumblebug)
	@chmod +x ./deployments/docker-compose/scripts/multi-init.sh 2>/dev/null || true
	@./deployments/docker-compose/scripts/multi-init.sh

init-openbao: ## Initialize OpenBao (one-time setup: generate unseal key + root token)
	@echo "Initializing OpenBao..."
	@chmod +x ./deployments/docker-compose/openbao/openbao-init.sh 2>/dev/null || true
	@./deployments/docker-compose/openbao/openbao-init.sh

unseal: ## Unseal OpenBao (needed after every container restart)
	@echo "Trying to unseal OpenBao (if not already unsealed)..."
	@chmod +x ./deployments/docker-compose/openbao/openbao-unseal.sh 2>/dev/null || true
	@./deployments/docker-compose/openbao/openbao-unseal.sh || true

logs: ## Follow Docker Compose logs (docker compose logs -f)
	@cd deployments/docker-compose && docker compose logs -f

status: ## Show status of Docker Compose services (docker compose ps)
	@cd deployments/docker-compose && docker compose ps --format "table {{.Name}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}"

ps: status ## Alias for status

clean-db: compose-down ## Clean all database metadata and persistent data (excluding OpenBao)
	@echo "Running cleanDB script..."
	@chmod +x ./deployments/docker-compose/scripts/cleanDB.sh 2>/dev/null || true
	@./deployments/docker-compose/scripts/cleanDB.sh

clean-all: compose-down clean-db ## Full reset including OpenBao (requires re-init)
	@echo "Cleaning OpenBao configuration and secrets..."
	@sudo rm -rf deployments/docker-compose/data/openbao-data/
	@sudo rm -rf deployments/docker-compose/data/openbao-honeybee-container/
	@find deployments/docker-compose/openbao/secrets -type f ! -name ".gitkeep" -delete
	@sed -i 's/^VAULT_TOKEN=.*/VAULT_TOKEN=/' deployments/docker-compose/.env 2>/dev/null || true
	@echo "Cleaned! Run 'make up' to re-initialize."

# Dummy targets to prevent make from interpreting branch names as targets
%:
	@:

help: ## Display this help screen
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'
