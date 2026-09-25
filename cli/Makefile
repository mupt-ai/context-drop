CLI_BINARY := context-drop
SERVER_BINARY := context-drop-server
CLI_CMD := ./cmd/context-drop
SERVER_CMD := ./cmd/context-drop-server
PREFIX ?= $(HOME)/.local
INSTALL_DIR ?= $(PREFIX)/bin
COVERAGE_PROFILE ?= coverage.out

.PHONY: build build-cli build-server install run run-server test race validate coverage fmt tidy clean

build: build-cli build-server

build-cli:
	go build -o bin/$(CLI_BINARY) $(CLI_CMD)

build-server:
	go build -o bin/$(SERVER_BINARY) $(SERVER_CMD)

install: build-cli
	mkdir -p $(INSTALL_DIR)
	install -m 0755 bin/$(CLI_BINARY) $(INSTALL_DIR)/$(CLI_BINARY)

run:
	go run $(CLI_CMD)

run-server:
	go run $(SERVER_CMD)

test:
	go test ./...
	python3 -m unittest discover -s jobs/herdr-activity-summary -p 'test_*.py'
	python3 -m unittest discover -s jobs/health-alerts -p 'test_*.py'
	node --test jobs/herdr-tab-namer/namer.test.mjs

race:
	go test -race ./...

validate: fmt test race

coverage:
	go test ./... -covermode=atomic -coverprofile=$(COVERAGE_PROFILE)
	go tool cover -func=$(COVERAGE_PROFILE) | tail -1

fmt:
	gofmt -s -w cmd internal

tidy:
	go mod tidy

clean:
	rm -rf bin coverage.out
