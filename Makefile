GO ?= go
BINARY ?= proxy
LISTEN ?= 127.0.0.1:1080
GATEWAY_IP ?=
GATEWAY_PORT ?= 1080
GOCACHE ?= $(CURDIR)/.gocache

BUILD_FLAGS ?= -trimpath
LD_FLAGS ?= -s -w
GO_ENV := GOCACHE=$(GOCACHE)

.PHONY: all build test fmt tidy run clean help

all: build

build:
	$(GO_ENV) $(GO) build $(BUILD_FLAGS) -ldflags "$(LD_FLAGS)" -o $(BINARY) .

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
	rm -rf $(GOCACHE)

help:
	@echo "Targets:"
	@echo "  make build    Build ./$(BINARY)"
	@echo "  make test     Run tests"
	@echo "  make fmt      Format Go code"
	@echo "  make tidy     Tidy Go modules"
	@echo "  make run      Run proxy with LISTEN/GATEWAY_IP/GATEWAY_PORT overrides"
	@echo "  make clean    Remove build output and local Go cache"
