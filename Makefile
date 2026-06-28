GO ?= go
BINARY ?= bin/proxy
DIST_DIR ?= dist
LISTEN ?= 127.0.0.1:1080
GATEWAY_IP ?=
GATEWAY_PORT ?= 1080
GOCACHE ?= $(CURDIR)/.gocache
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

BUILD_FLAGS ?= -trimpath
LD_FLAGS ?= -s -w
GO_ENV := GOCACHE=$(GOCACHE)
RELEASE_TARGETS ?= linux/amd64 linux/arm64 linux/arm/7 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: all build release test fmt tidy run clean help

all: build

build:
	$(GO_ENV) $(GO) build $(BUILD_FLAGS) -ldflags "$(LD_FLAGS)" -o $(BINARY) .

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
			GOOS=$${GOOS} GOARCH=$${GOARCH} GOARM=$${GOARM} $(GO_ENV) $(GO) build $(BUILD_FLAGS) -ldflags "$(LD_FLAGS)" -o $(DIST_DIR)/$${name} .; \
		else \
			GOOS=$${GOOS} GOARCH=$${GOARCH} $(GO_ENV) $(GO) build $(BUILD_FLAGS) -ldflags "$(LD_FLAGS)" -o $(DIST_DIR)/$${name} .; \
		fi; \
	done

test:
	$(GO_ENV) $(GO) test ./...

fmt:
	$(GO) fmt ./...

tidy:
	$(GO) mod tidy

run:
	$(GO_ENV) $(GO) run . --listen $(LISTEN) --gateway-port $(GATEWAY_PORT) $(if $(GATEWAY_IP),--gateway-ip $(GATEWAY_IP),)

clean:
	rm -f $(BINARY)
	rm -rf $(DIST_DIR)
	rm -rf $(GOCACHE)

help:
	@echo "Targets:"
	@echo "  make build    Build ./$(BINARY)"
	@echo "  make release  Cross-compile release binaries into ./$(DIST_DIR)"
	@echo "  make test     Run tests"
	@echo "  make fmt      Format Go code"
	@echo "  make tidy     Tidy Go modules"
	@echo "  make run      Run proxy with LISTEN/GATEWAY_IP/GATEWAY_PORT overrides"
	@echo "  make clean    Remove build output, release output, and local Go cache"
	@echo ""
	@echo "Release targets: $(RELEASE_TARGETS)"
