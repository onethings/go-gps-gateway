# Build, test and install helpers. `make` on its own lists the targets.
GO       ?= go
BIN_DIR  ?= bin
PREFIX   ?= $(HOME)/.local
BINARIES := traccar-gateway device-sim
WEB_DIR  ?= web

.DEFAULT_GOAL := help
.PHONY: help build test race vet fmt smoke rest-e2e run run-db run-sqlite web web-check web-e2e \
	install uninstall docker-check db-up db-down db-up-mysql db-down-mysql docker-build \
	compose-up compose-down compose-up-mysql compose-down-mysql dist clean

help: ## list the targets
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[1m%-14s\033[0m %s\n", $$1, $$2}'

build: web-check ## build both binaries into ./bin
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -o $(BIN_DIR)/ ./cmd/...

web: ## install a traccar-web build into ./web (WEB_SRC=dir-or-zip) or validate the bundled one
	@if [ -n "$(WEB_SRC)" ]; then ./scripts/fetch-web.sh --from "$(WEB_SRC)" --dest "$(WEB_DIR)"; \
	else ./scripts/fetch-web.sh --check --dest "$(WEB_DIR)"; fi

web-check: ## validate that ./web is a complete traccar-web build
	@./scripts/fetch-web.sh --check --dest "$(WEB_DIR)"

web-e2e: ## check the bundled frontend, same-origin login and a device position
	$(GO) test -count=1 ./internal/api -run TestBundledWebServesLoginAndPositions -v

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

rest-e2e: build ## replay the scenes against PostgreSQL or MySQL (DSN) and check positions, alerts and commands
	BIN_DIR=$(BIN_DIR) REST_E2E_NO_BUILD=1 ./scripts/rest-e2e.sh

run: build ## run with no database: positions are logged as JSON
	./$(BIN_DIR)/traccar-gateway

run-db: build ## run against a database (TRACCAR_DSN=postgres:// / mysql:// / sqlite://)
	./$(BIN_DIR)/traccar-gateway -dsn "$${DSN:-$${TRACCAR_DSN}}" -web-path "$(CURDIR)/$(WEB_DIR)"

run-sqlite: build ## run against the embedded SQLite file ./traccar.db
	./$(BIN_DIR)/traccar-gateway -dsn "sqlite://$(CURDIR)/traccar.db" -web-path "$(CURDIR)/$(WEB_DIR)"

install: build ## install the binaries into $(PREFIX)/bin
	@mkdir -p "$(DESTDIR)$(PREFIX)/bin"
	install -m 0755 $(addprefix $(BIN_DIR)/,$(BINARIES)) "$(DESTDIR)$(PREFIX)/bin"
	@echo "installed into $(DESTDIR)$(PREFIX)/bin"

uninstall: ## remove the installed binaries
	rm -f $(addprefix $(DESTDIR)$(PREFIX)/bin/,$(BINARIES))

docker-check: ## fail early when the docker daemon is not reachable
	@docker info >/dev/null 2>&1 || { \
		echo "docker daemon unreachable ($(shell docker context show 2>/dev/null || echo unknown) context)."; \
		echo "start Docker Desktop with: open -a Docker"; \
		echo "or run your own PostgreSQL and pass -dsn / TRACCAR_DSN instead."; \
		exit 1; }

db-up: docker-check ## start a throwaway PostgreSQL on 55432
	docker run --rm -d --name traccar-pg -p 55432:5432 \
		-e POSTGRES_PASSWORD=traccar -e POSTGRES_DB=traccar postgres:16

db-down: ## stop the throwaway PostgreSQL
	-docker rm -f traccar-pg

db-up-mysql: docker-check ## start a throwaway MySQL 8.0 on 53306
	docker run --rm -d --name traccar-mysql -p 53306:3306 \
		-e MYSQL_ROOT_PASSWORD=traccar -e MYSQL_DATABASE=traccar mysql:8.0

db-down-mysql: ## stop the throwaway MySQL
	-docker rm -f traccar-mysql

docker-build: docker-check ## build the container image
	docker build -t traccar-server-go:dev .

compose-up: docker-check ## start gateway + PostgreSQL with docker compose
	docker compose up --build -d

compose-down: ## stop the compose stack and drop its volume
	docker compose down -v

compose-up-mysql: docker-check ## start gateway + MySQL with traccar-web in ./web
	docker compose -f docker-compose.mysql.yml up --build -d

compose-down-mysql: ## stop the MySQL stack and drop its volume
	docker compose -f docker-compose.mysql.yml down -v

dist: ## build the self-contained Linux release zip into ./dist (amd64 + arm64)
	GO=$(GO) VERSION=$(VERSION) ./scripts/package-release.sh

clean: ## remove ./bin and ./dist
	rm -rf $(BIN_DIR) dist
