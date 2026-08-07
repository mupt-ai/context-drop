CLI_BINARY := context-drop
SERVER_BINARY := context-drop-server
CLI_CMD := ./cmd/context-drop
SERVER_CMD := ./cmd/context-drop-server
PREFIX ?= $(HOME)/.local
INSTALL_DIR ?= $(PREFIX)/bin
LIB_DIR ?= $(PREFIX)/lib/context-drop
COVERAGE_PROFILE ?= coverage.out

.PHONY: build build-cli build-server runtime-install runtime-build install install-smoke daemon-smoke imessage-smoke smoke run run-server test race validate coverage fmt tidy clean

build: build-cli build-server runtime-build

build-cli:
	go build -o bin/$(CLI_BINARY) $(CLI_CMD)

build-server:
	go build -o bin/$(SERVER_BINARY) $(SERVER_CMD)

runtime-install:
	cd runtime && npm ci

runtime-build:
	cd runtime && npm run build

install: build
	mkdir -p $(INSTALL_DIR) $(LIB_DIR)/runtime
	install -m 0755 bin/$(CLI_BINARY) $(INSTALL_DIR)/$(CLI_BINARY)
	cp -R runtime/dist $(LIB_DIR)/runtime/

install-smoke:
	./scripts/install-smoke.sh

daemon-smoke: build
	./scripts/daemon-smoke.sh

imessage-smoke: build
	./scripts/imessage-smoke.sh

smoke: install-smoke daemon-smoke imessage-smoke

run:
	go run $(CLI_CMD)

run-server:
	go run $(SERVER_CMD)

test:
	go test ./...
	cd runtime && npm test

race:
	go test -race ./...

validate: fmt test race
	cd runtime && npm run validate
	./scripts/install-smoke.sh

coverage:
	go test ./... -covermode=atomic -coverprofile=$(COVERAGE_PROFILE)
	go tool cover -func=$(COVERAGE_PROFILE) | tail -1

fmt:
	gofmt -s -w cmd internal

tidy:
	go mod tidy

clean:
	rm -rf bin runtime/dist runtime/node_modules coverage.out
