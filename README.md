# Proxy

Local mixed proxy forwarder written in Go.

This tool opens a local mixed proxy port and forwards every accepted TCP connection unchanged to a mixed proxy port on the gateway. It is designed for low overhead: protocol bytes are not parsed or rewritten on the hot path, and connection forwarding uses pooled copy buffers.

English | [简体中文](README.zh-CN.md)

## Features

- Listens locally on `127.0.0.1:1080` by default.
- Forwards to the gateway mixed proxy port `1080` by default.
- Supports mixed proxy traffic such as SOCKS5, HTTP proxy, and HTTP CONNECT when the upstream gateway port supports them.
- Supports SOCKS5 UDP ASSOCIATE for UDP relay traffic.
- Prints compact terminal access logs; direct connections omit the proxy field.
- Auto-detects the default gateway IP.
- Checks whether the detected gateway port is reachable.
- Scans local IPv4 networks when the detected gateway is unreachable.
- Periodically refreshes the reachable upstream so new connections follow network changes.
- Connects directly to private, loopback, link-local, `localhost`, and `.local` targets instead of forwarding them upstream.
- Tries direct TCP connections first; if a target cannot be reached directly, remembers that target and sends later connections upstream immediately.
- Supports `config.json` force-upstream rules by exact domain, domain prefix, domain suffix, exact IP, and IP CIDR/range.
- Writes learned direct-failure targets back to `config.json` before exit, creating the file when needed and deduplicating existing rules.
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

Use a different route config:

```sh
bin/proxy --config ./config.json
```

## Gateway Discovery

When `--gateway-ip` is not set, startup works like this:

1. Detect the system default gateway IP.
2. Try to connect to `<gateway-ip>:<gateway-port>`.
3. If that connection fails, scan local IPv4 networks for a host with `<gateway-port>` open.
4. Use the first reachable host as the upstream mixed proxy.

Manual `--gateway-ip` disables scanning and uses the provided IP directly.

While running, the proxy checks local IPv4 addresses every `--refresh-interval`. Gateway discovery and local-network scanning only run when the local IPv4 address set changes. Existing connections continue on their current upstream; new connections use the refreshed target after a change is detected.

## Internal Address Bypass

For SOCKS5, SOCKS5 UDP ASSOCIATE, HTTP CONNECT, and HTTP proxy requests, the proxy inspects the requested target. Force-upstream rules in `config.json` have the highest priority. Otherwise, TCP targets are tried directly first. If direct TCP connection fails, that target is remembered as upstream-only and later connections skip the direct attempt. UDP targets keep the conservative rule: internal targets go direct, other targets go upstream.

## Route Config

By default the proxy tries to read `config.json` next to the executable. Relative `--config` paths are resolved from the executable directory; absolute paths are used as provided. The included `config.json` sends `x.com`, `twitter.com`, and related subdomains upstream. If that file does not exist, the proxy runs without custom route rules and creates it before exit when new direct failures are learned. Use `--config <path>` to choose another file, or `--config ""` to disable config loading and write-back.

Example:

```json
{
  "force_upstream": {
    "domains": ["x.com", "twitter.com"],
    "domain_prefixes": ["api.", "pbs.twimg."],
    "domain_suffixes": ["x.com", "twitter.com"],
    "ips": ["8.8.8.8"],
    "ip_cidrs": ["1.1.1.0/24", "2001:4860:4860::/48"],
    "ip_ranges": ["203.0.113.0/24"]
  }
}
```

Rule behavior:

- `domains`: exact host match.
- `domain_prefixes`: host starts with the configured value.
- `domain_suffixes`: matches the domain itself and its subdomains.
- `ips`: exact IP match.
- `ip_cidrs` and `ip_ranges`: CIDR prefix match. `ip_ranges` is an alias for CIDR-style ranges.

Before exit, learned direct TCP failures are merged into this file. Failed domain targets are appended to `domains`, and failed IP targets are appended to `ips`. If an existing exact domain, domain prefix, domain suffix, exact IP, or IP CIDR/range already covers the target, nothing is added.

## UDP Support

UDP is supported through SOCKS5 UDP ASSOCIATE. The TCP mixed proxy port negotiates a UDP relay address, then UDP datagrams use the standard SOCKS5 UDP packet header. Internal UDP targets are sent directly from the local relay; non-internal UDP targets are relayed through the upstream gateway's SOCKS5 UDP support.

## Access Logs

The proxy prints one access line for each routed TCP connection and SOCKS5 UDP datagram. The source includes the detected proxy protocol and a friendly local address. Proxied traffic includes the upstream proxy address; direct traffic omits that middle field. The final field is `ok` or the failure reason.

```text
http-connect/localhost:53000 -> 10.207.20.78:1080 -> x.com:443 ok
socks5/localhost:53001 -> 192.168.1.10:80 ok
socks5-udp/localhost:53002 -> 10.207.20.78:1080 -> 8.8.8.8:53 ok
```

## Options

```text
--buffer-size <int>         per-direction copy buffer size in bytes [default: 32768]
-c, --config <string>       JSON route config path; empty disables config loading [default: "config.json"]
--dial-timeout <duration>   upstream dial timeout [default: 5s]
--gateway-ip <string>       gateway IP; empty means auto-detect
-p, --gateway-port <int>    gateway mixed proxy port [default: 1080]
-l, --listen <string>       local listen address [default: "127.0.0.1:1080"]
--refresh-interval <duration> interval for checking local IPv4 changes; 0 disables refresh [default: 5s]
--scan-timeout <duration>   per-IP timeout when scanning local IPv4 networks [default: 250ms]
--scan-workers <int>        parallel workers used for IPv4 network scanning
-v, --verbose               enable debug logs
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

`make run` uses this repository's `config.json` by default and accepts overrides:

```sh
make run LISTEN=127.0.0.1:1081 GATEWAY_PORT=7890
make run GATEWAY_IP=192.168.1.1
make run CONFIG=/path/to/config.json
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
