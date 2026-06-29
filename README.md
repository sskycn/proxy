# Proxy

Local mixed proxy forwarder written in Go.

This tool opens a local mixed proxy port and forwards upstream traffic through the gateway. The upstream protocol is configurable and defaults to SOCKS5. It is designed for low overhead: connection forwarding uses pooled copy buffers, while HTTP and SOCKS5 requests are parsed only enough to choose direct or upstream routing.

English | [简体中文](README.zh-CN.md)

## Features

- Listens locally on `127.0.0.1:1080` by default.
- Forwards to the gateway proxy port `1080` by default.
- Accepts mixed local proxy traffic such as SOCKS5, HTTP proxy, and HTTP CONNECT.
- Uses SOCKS5 for upstream traffic by default; `mixed` upstream mode is also supported.
- Supports `proxy`, `proxy client`, and `proxy server` commands with a compact custom tunnel protocol.
- Carries the client/server tunnel over raw TCP, WebSocket, HTTP/2, or HTTP/3 transport.
- Multiplexes client/server tunnel streams by default, so many TCP connections and UDP relays can share one upstream tunnel transport connection.
- Supports SOCKS5 UDP ASSOCIATE for UDP relay traffic.
- Prints compact terminal access logs; direct connections omit the proxy field.
- Auto-detects the default gateway IP.
- Checks whether the detected gateway port is reachable only when the machine has an internal IPv4 address.
- Scans internal local IPv4 networks when the detected gateway is unreachable.
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
go build -trimpath -ldflags "-s -w" -o bin/proxy ./cmd/proxy
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

Use a different gateway proxy port:

```sh
bin/proxy --gateway-port 7890
```

Use a mixed upstream gateway instead of the default SOCKS5 upstream:

```sh
bin/proxy --upstream-protocol mixed
```

Use a different route config:

```sh
bin/proxy --config ./config.json
```

Run as a tunnel server:

```sh
bin/proxy server --listen 0.0.0.0:9443 --token change-me
```

Run as a tunnel client:

```sh
bin/proxy client --listen 127.0.0.1:1080 --server-addr 203.0.113.10:9443 --token change-me
```

Run through an HTTP reverse proxy or CDN with WebSocket:

```sh
bin/proxy server --listen 127.0.0.1:9443 --transport ws --tunnel-path /proxy --token change-me
bin/proxy client --listen 127.0.0.1:1080 --server-addr proxy.example.com:443 --transport ws --tunnel-path /proxy --tls --token change-me
```

Run over HTTP/2:

```sh
bin/proxy server --listen 127.0.0.1:9443 --transport h2 --tunnel-path /proxy --token change-me
bin/proxy client --server-addr 127.0.0.1:9443 --transport h2 --tunnel-path /proxy --token change-me
```

Run over HTTP/3 with TLS certificates:

```sh
bin/proxy server --listen 0.0.0.0:9443 --transport h3 --tunnel-path /proxy --tls-cert server.crt --tls-key server.key --token change-me
bin/proxy client --server-addr proxy.example.com:9443 --transport h3 --tunnel-path /proxy --token change-me
```

## Gateway Discovery

When `--gateway-ip` is not set, startup works like this:

1. Check whether the machine has an internal IPv4 address.
2. If it does, detect the system default gateway IP.
3. Try to connect to `<gateway-ip>:<gateway-port>`.
4. If that connection fails, scan internal local IPv4 networks for a host with `<gateway-port>` open.
5. Use the first reachable host as the upstream proxy.

If the machine has no internal IPv4 address, automatic gateway probing and local IPv4 scanning are skipped. Set `--gateway-ip` explicitly in that case.

Manual `--gateway-ip` disables scanning and uses the provided IP directly.

While running, the proxy checks local IPv4 addresses every `--refresh-interval`. Gateway discovery and local-network scanning only run when the local IPv4 address set changes. Existing connections continue on their current upstream; new connections use the refreshed target after a change is detected.

## Internal Address Bypass

For SOCKS5, SOCKS5 UDP ASSOCIATE, HTTP CONNECT, and HTTP proxy requests, the proxy inspects the requested target. Force-upstream rules in `config.json` have the highest priority. Otherwise, TCP targets are tried directly first. If direct TCP connection fails, that target is remembered as upstream-only and later connections skip the direct attempt. UDP targets keep the conservative rule: internal targets go direct, other targets go upstream.

## Upstream Protocol

The upstream protocol can be configured with `--upstream-protocol` or the top-level `upstream_protocol` field in `config.json`. Supported values are `socks5` and `mixed`; the default is `socks5`.

In `socks5` mode, SOCKS5 and HTTP proxy traffic with a parsed target is converted to SOCKS5 before going upstream. Unknown mixed traffic is rejected because it has no parsed target.

In `mixed` mode, HTTP proxy traffic and unknown mixed traffic are forwarded to the gateway unchanged. SOCKS5 TCP and UDP still use SOCKS5 negotiation, so the upstream mixed port must support SOCKS5.

## Client/Server Commands

Running `proxy` without a subcommand keeps the original local behavior: the local mixed proxy forwards through the discovered gateway proxy.

`proxy server` listens for this project's compact custom tunnel protocol and connects to the requested TCP or UDP target from the server side. Use `--listen` to choose the server bind address and `--token` to require a shared token.

`proxy client` keeps the local mixed proxy listener, but upstream TCP and UDP traffic with a parsed target is encapsulated to the tunnel server. Use `--server-addr` for the server address and the same `--token` value used by the server.

The tunnel transport is selected with `--transport` or `tunnel_transport` in `config.json`:

- `raw`: direct TCP connection to the server. This is the default and has the least overhead.
- `ws`: WebSocket over HTTP/1.1. This is the most practical option behind nginx HTTP reverse proxy or common CDNs.
- `h2`: HTTP/2 bidirectional request/response stream. Without server certificates it runs as h2c; with `--tls-cert` and `--tls-key` it serves TLS HTTP/2.
- `h3`: HTTP/3 over QUIC. The server requires `--tls-cert` and `--tls-key`, and the client always uses `https`.

The custom tunnel is intentionally small and currently carries TCP streams and SOCKS5 UDP packets. It is not VLESS-compatible.

Tunnel multiplexing is enabled by default. With multiplexing enabled, `proxy client` keeps a shared tunnel transport connection to `proxy server`, then opens one logical stream for each proxied TCP connection or UDP relay. This reduces WebSocket/HTTP/2/HTTP/3 handshakes and works better behind HTTP/CDN infrastructure. Use `--mux=false` or `"tunnel_mux": false` to fall back to one tunnel transport connection per proxied stream.

### nginx WebSocket Example

For nginx HTTP reverse proxy, run the server on loopback:

```sh
bin/proxy server --listen 127.0.0.1:9443 --transport ws --tunnel-path /proxy --token change-me
```

Then proxy a WebSocket location:

```nginx
location /proxy {
    proxy_pass http://127.0.0.1:9443;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
}
```

The client should connect to the public HTTPS name:

```sh
bin/proxy client --server-addr proxy.example.com:443 --transport ws --tunnel-path /proxy --tls --token change-me
```

## Route Config

By default the proxy tries to read `config.json` next to the executable. Relative `--config` paths are resolved from the executable directory; absolute paths are used as provided. The included `config.json` sends `x.com`, `twitter.com`, and related subdomains upstream. If that file does not exist, the proxy runs without custom route rules and creates it before exit when new direct failures are learned. Use `--config <path>` to choose another file, or `--config ""` to disable config loading and write-back.

Example:

```json
{
  "upstream_protocol": "socks5",
  "server_addr": "",
  "token": "",
  "tunnel_transport": "raw",
  "tunnel_path": "/proxy",
  "tunnel_tls": false,
  "tunnel_tls_server_name": "",
  "tunnel_tls_insecure": false,
  "tunnel_mux": true,
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

The proxy prints one access line for each routed TCP connection and SOCKS5 UDP datagram. The source includes the detected proxy protocol and a friendly local address; HTTP CONNECT is logged as `httpc`, while normal HTTP proxy traffic is logged as `http`. Proxied traffic includes the upstream proxy address; direct traffic omits that middle field. The final field is `ok` or the failure reason.

```text
httpc/localhost:53000 -> 10.207.20.78:1080 -> x.com:443 ok
http/localhost:53001 -> 192.168.1.10:80 ok
socks5-udp/localhost:53002 -> 10.207.20.78:1080 -> 8.8.8.8:53 ok
```

## Options

```text
--buffer-size <int>         per-direction copy buffer size in bytes [default: 32768]
-c, --config <string>       JSON route config path; empty disables config loading [default: "config.json"]
--dial-timeout <duration>   upstream dial timeout [default: 5s]
--gateway-ip <string>       gateway IP; empty means auto-detect
-p, --gateway-port <int>    gateway proxy port [default: 1080]
-l, --listen <string>       local listen address [default: "127.0.0.1:1080"]
--refresh-interval <duration> interval for checking local IPv4 changes; 0 disables refresh [default: 5s]
--scan-timeout <duration>   per-IP timeout when scanning local IPv4 networks [default: 250ms]
--scan-workers <int>        parallel workers used for IPv4 network scanning
--upstream-protocol <string> upstream protocol: socks5 or mixed [default: socks5]
-v, --verbose               enable debug logs
```

`proxy client` adds:

```text
--server-addr <string>      custom tunnel server address
--token <string>            shared token for custom tunnel auth
--transport <string>        tunnel transport: raw, ws, h2, or h3 [default: raw]
--tunnel-path <string>      HTTP/WebSocket tunnel path [default: /proxy]
--tls                       use TLS for ws/h2 transport
--tls-server-name <string>  TLS server name override
--tls-insecure              skip TLS certificate verification
--mux <bool>                enable tunnel multiplexing [default: true]
```

`proxy server` adds:

```text
--token <string>            shared token for custom tunnel auth
--transport <string>        tunnel transport: raw, ws, h2, or h3 [default: raw]
--tunnel-path <string>      HTTP/WebSocket tunnel path [default: /proxy]
--tls-cert <string>         TLS certificate file for h2/h3 server
--tls-key <string>          TLS private key file for h2/h3 server
--mux <bool>                enable tunnel multiplexing [default: true]
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
make run UPSTREAM_PROTOCOL=mixed
make run MODE=server LISTEN=0.0.0.0:9443 TOKEN=change-me
make run MODE=client SERVER_ADDR=203.0.113.10:9443 TOKEN=change-me
make run MODE=server LISTEN=127.0.0.1:9443 TRANSPORT=ws TUNNEL_PATH=/proxy TOKEN=change-me
make run MODE=client SERVER_ADDR=proxy.example.com:443 TRANSPORT=ws TUNNEL_PATH=/proxy TLS=1 TOKEN=change-me
make run MODE=client SERVER_ADDR=proxy.example.com:443 TRANSPORT=ws MUX=false TOKEN=change-me
```

`MODE=server` and `MODE=client` are Makefile shortcuts that run `proxy server` and `proxy client`.

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
