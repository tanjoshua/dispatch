SHELL := /usr/bin/env bash

.PHONY: up infra migrate server worker web down help

help:
	@echo "make up      Start the complete development environment"
	@echo "make down    Stop Postgres and Temporal"
	@echo "make infra   Start Postgres and Temporal"
	@echo "make migrate Apply database migrations"
	@echo "make server  Start the Go API"
	@echo "make worker  Start the Temporal worker"
	@echo "make web     Start the Vite frontend"

# Start dependencies first and wait for their Docker health checks before
# migrations or application processes try to connect.
infra:
	docker compose up -d --wait

migrate: infra
	go run ./cmd/migrate

server:
	go run ./cmd/server

worker:
	go run ./cmd/worker

web/node_modules: web/package.json web/package-lock.json
	cd web && npm ci
	@touch web/node_modules

web: web/node_modules
	cd web && npm run dev

# Postgres and Temporal remain running after Ctrl-C so subsequent starts are
# fast. Use `make down` when the infrastructure should also be stopped.
up: migrate web/node_modules
	@set -euo pipefail; \
	cleanup() { \
		trap - INT TERM EXIT; \
		kill "$$server_pid" "$$worker_pid" "$$web_pid" 2>/dev/null || true; \
		wait "$$server_pid" "$$worker_pid" "$$web_pid" 2>/dev/null || true; \
	}; \
	trap cleanup EXIT; \
	trap 'exit 130' INT TERM; \
	go run ./cmd/server & server_pid=$$!; \
	go run ./cmd/worker & worker_pid=$$!; \
	(cd web && npm run dev) & web_pid=$$!; \
	echo "Dispatch dev environment is running"; \
	echo "  App:      http://localhost:5173"; \
	echo "  API:      http://localhost:8080"; \
	echo "  Temporal: http://localhost:8233"; \
	echo "Press Ctrl-C to stop the app processes."; \
	wait -n "$$server_pid" "$$worker_pid" "$$web_pid"

down:
	docker compose down
