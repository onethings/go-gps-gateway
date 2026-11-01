# Build, test and install helpers. `make` on its own lists the targets.
GO       ?= go
BIN_DIR  ?= bin
PREFIX   ?= $(HOME)/.local
BINARIES := traccar-gateway device-sim

.DEFAULT_GOAL := help
.PHONY: help build test race vet fmt smoke run run-db install uninstall \
	db-up db-down docker-build compose-up compose-down clean

help: ## list the targets
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[1m%-14s\033[0m %s\n", $$1, $$2}'

build: ## build both binaries into ./bin
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -o $(BIN_DIR)/ ./cmd/...

test: ## run the whole suite
	$(GO) test ./...

race: ## run the concurrent packages under the race detector
	$(GO) test -race ./internal/gateway ./internal/alert ./internal/api ./internal/notify

vet: ## go vet
	$(GO) vet ./...

fmt: ## go fmt
	$(GO) fmt ./...

smoke: build ## replay the four protocol scenes against a throwaway gateway
	BIN_DIR=$(BIN_DIR) SMOKE_NO_BUILD=1 ./scripts/smoke.sh

run: build ## run with no database: positions are logged as JSON
	./$(BIN_DIR)/traccar-gateway

run-db: build ## run against PostgreSQL (set TRACCAR_DSN, or pass DSN=...)
	./$(BIN_DIR)/traccar-gateway -dsn "$${DSN:-$${TRACCAR_DSN}}"

install: build ## install the binaries into $(PREFIX)/bin
	@mkdir -p "$(DESTDIR)$(PREFIX)/bin"
	install -m 0755 $(addprefix $(BIN_DIR)/,$(BINARIES)) "$(DESTDIR)$(PREFIX)/bin"
	@echo "installed into $(DESTDIR)$(PREFIX)/bin"

uninstall: ## remove the installed binaries
	rm -f $(addprefix $(DESTDIR)$(PREFIX)/bin/,$(BINARIES))

db-up: ## start a throwaway PostgreSQL on 55432
	docker run --rm -d --name traccar-pg -p 55432:5432 \
		-e POSTGRES_PASSWORD=traccar -e POSTGRES_DB=traccar postgres:16

db-down: ## stop the throwaway PostgreSQL
	-docker rm -f traccar-pg

docker-build: ## build the container image
	docker build -t traccar-server-go:dev .

compose-up: ## start gateway + PostgreSQL with docker compose
	docker compose up --build -d

compose-down: ## stop the compose stack and drop its volume
	docker compose down -v

clean: ## remove ./bin
	rm -rf $(BIN_DIR)
