# Proxy

Local mixed proxy forwarder written in Go.

This tool opens a local mixed proxy port and forwards every accepted TCP connection unchanged to a mixed proxy port on the gateway. It is designed for low overhead: protocol bytes are not parsed or rewritten on the hot path, and connection forwarding uses pooled copy buffers.

English | [简体中文](README.zh-CN.md)

## Features

- Listens locally on `127.0.0.1:1080` by default.
- Forwards to the gateway mixed proxy port `1080` by default.
- Supports mixed proxy traffic such as SOCKS5, HTTP proxy, and HTTP CONNECT when the upstream gateway port supports them.
- Auto-detects the default gateway IP.
- Checks whether the detected gateway port is reachable.
- Scans local IPv4 networks when the detected gateway is unreachable.
- Periodically refreshes the reachable upstream so new connections follow network changes.
- Uses `pkg.gostartkit.com/cmd v0.2.1` for the command-line interface.

## Requirements

- Go 1.24 or newer.

## Build

```sh
make build
```

The binary is written to:

```text
bin/proxy
```

You can also build directly with Go:

```sh
go build -trimpath -ldflags "-s -w" -o bin/proxy .
```

## Run

Start with automatic gateway discovery:

```sh
make run
```

Or run the built binary:

```sh
bin/proxy
```

Use a different local port:

```sh
bin/proxy --listen 127.0.0.1:1081
```

Use a known gateway IP:

```sh
bin/proxy --gateway-ip 192.168.1.1
```

Use a different gateway mixed port:

```sh
bin/proxy --gateway-port 7890
```

## Gateway Discovery

When `--gateway-ip` is not set, startup works like this:

1. Detect the system default gateway IP.
2. Try to connect to `<gateway-ip>:<gateway-port>`.
3. If that connection fails, scan local IPv4 networks for a host with `<gateway-port>` open.
4. Use the first reachable host as the upstream mixed proxy.

Manual `--gateway-ip` disables scanning and uses the provided IP directly.

While running, the proxy refreshes the reachable upstream every `--refresh-interval`. Existing connections continue on their current upstream; new connections use the refreshed target.

## Options

```text
--buffer-size <int>         per-direction copy buffer size in bytes [default: 32768]
--dial-timeout <duration>   upstream dial timeout [default: 5s]
--gateway-ip <string>       gateway IP; empty means auto-detect
-p, --gateway-port <int>    gateway mixed proxy port [default: 1080]
-l, --listen <string>       local listen address [default: "127.0.0.1:1080"]
--refresh-interval <duration> interval for refreshing the reachable upstream; 0 disables refresh [default: 5s]
--scan-timeout <duration>   per-IP timeout when scanning local IPv4 networks [default: 250ms]
--scan-workers <int>        parallel workers used for IPv4 network scanning
-v, --verbose               enable connection logs
```

## Make Targets

```sh
make build    # Build bin/proxy
make test     # Run tests
make fmt      # Format Go code
make tidy     # Tidy Go modules
make run      # Run with Makefile defaults
make clean    # Remove build output and local Go cache
```

`make run` accepts overrides:

```sh
make run LISTEN=127.0.0.1:1081 GATEWAY_PORT=7890
make run GATEWAY_IP=192.168.1.1
```

## Development

Run tests:

```sh
make test
```

Format and tidy:

```sh
make fmt
make tidy
```

Clean generated files:

```sh
make clean
```
