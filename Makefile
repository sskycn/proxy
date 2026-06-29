GO ?= go
BINARY ?= bin/proxy
CMD_PKG ?= ./cmd/proxy
DIST_DIR ?= dist
LISTEN ?= 127.0.0.1:1080
MODE ?=
SERVER_ADDR ?=
TOKEN ?=
TRANSPORT ?=
TUNNEL_PATH ?=
TLS ?=
TLS_SERVER_NAME ?=
TLS_INSECURE ?=
TLS_CERT ?=
TLS_KEY ?=
MUX ?=
GATEWAY_IP ?=
GATEWAY_PORT ?= 1080
UPSTREAM_PROTOCOL ?=
CONFIG ?= $(CURDIR)/config.json
GOCACHE ?= $(CURDIR)/.gocache
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

BUILD_FLAGS ?= -trimpath
LD_FLAGS ?= -s -w
GO_ENV := GOCACHE=$(GOCACHE)
RELEASE_TARGETS ?= linux/amd64 linux/arm64 linux/arm/7 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

ifeq ($(MODE),server)
RUN_COMMAND := server
RUN_FLAGS := --listen $(LISTEN) --config $(CONFIG) $(if $(TOKEN),--token $(TOKEN),) $(if $(TRANSPORT),--transport $(TRANSPORT),) $(if $(TUNNEL_PATH),--tunnel-path $(TUNNEL_PATH),) $(if $(TLS_CERT),--tls-cert $(TLS_CERT),) $(if $(TLS_KEY),--tls-key $(TLS_KEY),) $(if $(MUX),--mux=$(MUX),)
else ifeq ($(MODE),client)
RUN_COMMAND := client
RUN_FLAGS := --listen $(LISTEN) --config $(CONFIG) $(if $(SERVER_ADDR),--server-addr $(SERVER_ADDR),) $(if $(TOKEN),--token $(TOKEN),) $(if $(TRANSPORT),--transport $(TRANSPORT),) $(if $(TUNNEL_PATH),--tunnel-path $(TUNNEL_PATH),) $(if $(TLS),--tls,) $(if $(TLS_SERVER_NAME),--tls-server-name $(TLS_SERVER_NAME),) $(if $(TLS_INSECURE),--tls-insecure,) $(if $(MUX),--mux=$(MUX),)
else ifeq ($(MODE),local)
RUN_COMMAND := local
RUN_FLAGS := --listen $(LISTEN) --gateway-port $(GATEWAY_PORT) --config $(CONFIG) $(if $(UPSTREAM_PROTOCOL),--upstream-protocol $(UPSTREAM_PROTOCOL),) $(if $(GATEWAY_IP),--gateway-ip $(GATEWAY_IP),)
else
RUN_COMMAND :=
RUN_FLAGS := --listen $(LISTEN) --gateway-port $(GATEWAY_PORT) --config $(CONFIG) $(if $(UPSTREAM_PROTOCOL),--upstream-protocol $(UPSTREAM_PROTOCOL),) $(if $(GATEWAY_IP),--gateway-ip $(GATEWAY_IP),)
endif

.PHONY: all build release test fmt tidy run clean help

all: build

build:
	$(GO_ENV) $(GO) build $(BUILD_FLAGS) -ldflags "$(LD_FLAGS)" -o $(BINARY) $(CMD_PKG)

release:
	@mkdir -p $(DIST_DIR)
	@set -e; \
	for target in $(RELEASE_TARGETS); do \
		GOOS=$${target%%/*}; \
		rest=$${target#*/}; \
		GOARCH=$${rest%%/*}; \
		GOARM=$${rest#*/}; \
		name=proxy-$${GOOS}-$${GOARCH}; \
		if [ "$${GOARM}" != "$${rest}" ]; then \
			name=$${name}v$${GOARM}; \
		fi; \
		if [ "$${GOOS}" = "windows" ]; then \
			name=$${name}.exe; \
		fi; \
		echo "building $(DIST_DIR)/$${name}"; \
		if [ "$${GOARM}" != "$${rest}" ]; then \
			GOOS=$${GOOS} GOARCH=$${GOARCH} GOARM=$${GOARM} $(GO_ENV) $(GO) build $(BUILD_FLAGS) -ldflags "$(LD_FLAGS)" -o $(DIST_DIR)/$${name} $(CMD_PKG); \
		else \
			GOOS=$${GOOS} GOARCH=$${GOARCH} $(GO_ENV) $(GO) build $(BUILD_FLAGS) -ldflags "$(LD_FLAGS)" -o $(DIST_DIR)/$${name} $(CMD_PKG); \
		fi; \
	done

test:
	$(GO_ENV) $(GO) test ./...

fmt:
	$(GO) fmt ./...

tidy:
	$(GO) mod tidy

run:
	$(GO_ENV) $(GO) run $(CMD_PKG) $(RUN_COMMAND) $(RUN_FLAGS)

clean:
	rm -rf $(dir $(BINARY))
	rm -rf $(DIST_DIR)
	rm -rf $(GOCACHE)

help:
	@echo "Targets:"
	@echo "  make build    Build ./$(BINARY)"
	@echo "  make release  Cross-compile release binaries into ./$(DIST_DIR)"
	@echo "  make test     Run tests"
	@echo "  make fmt      Format Go code"
	@echo "  make tidy     Tidy Go modules"
	@echo "  make run      Run proxy with LISTEN/MODE/SERVER_ADDR/TOKEN/TRANSPORT/TUNNEL_PATH/MUX/GATEWAY_IP/GATEWAY_PORT/UPSTREAM_PROTOCOL/CONFIG overrides"
	@echo "  make clean    Remove build output, release output, and local Go cache"
	@echo ""
	@echo "Release targets: $(RELEASE_TARGETS)"
