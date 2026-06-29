# Proxy

Local mixed proxy forwarder written in Go.

This tool opens a local mixed proxy port and forwards upstream traffic through the gateway. The upstream protocol is configurable and defaults to SOCKS5. It is designed for low overhead: connection forwarding uses pooled copy buffers, while HTTP and SOCKS5 requests are parsed only enough to choose direct or upstream routing.

English | [简体中文](README.zh-CN.md)

## Features

- Listens locally on `127.0.0.1:1080` by default.
- Forwards to the gateway proxy port `1080` by default.
- Accepts mixed local proxy traffic such as SOCKS5, HTTP proxy, and HTTP CONNECT.
- Supports username/password authentication for the local SOCKS5 listener.
- Uses SOCKS5 for upstream traffic by default; `mixed` upstream mode is also supported.
- Supports username/password authentication when dialing an upstream SOCKS5 gateway.
- Supports `proxy`, `proxy local`, `proxy client`, and `proxy server` commands with configurable tunnel protocols: `native`, `vless`, `vmess`, and `trojan`.
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
- Supports `route.json` force-upstream rules by exact domain, domain regex, domain suffix, exact IP, and IP CIDR/range.
- Writes learned direct-failure targets back to `route.json` before exit, creating the file when needed and deduplicating existing rules.
- Uses `pkg.gostartkit.com/cmd v0.2.1` for the command-line interface.

## Requirements

- Go 1.25 or newer.

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

Run explicitly in local mode, ignoring any `mode` value from `config.json`:

```sh
bin/proxy local
```

Use a different runtime config:

```sh
bin/proxy --config ./config.json
```

Use a different route config:

```sh
bin/proxy --route-config ./route.json
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

Run client/server mode with VLESS:

```sh
bin/proxy server --listen 0.0.0.0:9443 --tunnel-protocol vless --token 00000000-0000-4000-8000-000000000000
bin/proxy client --server-addr 203.0.113.10:9443 --tunnel-protocol vless --token 00000000-0000-4000-8000-000000000000
```

Connect to an Xray VLESS REALITY/Vision server. The values below are placeholders; do not commit real server addresses, UUIDs, public keys, or private keys to documentation:

```sh
bin/proxy client \
  --listen 127.0.0.1:1080 \
  --server-addr '[2001:db8::10]:443' \
  --tunnel-protocol vless \
  --transport raw \
  --tunnel-security reality \
  --flow xtls-rprx-vision \
  --token 00000000-0000-4000-8000-000000000000 \
  --reality-server-name example.com \
  --reality-fingerprint chrome \
  --reality-public-key REALITY_PUBLIC_KEY \
  --reality-short-id ''
```

Run an Xray-compatible VLESS REALITY/Vision server:

```sh
bin/proxy server \
  --listen 0.0.0.0:443 \
  --tunnel-protocol vless \
  --transport raw \
  --tunnel-security reality \
  --flow xtls-rprx-vision \
  --token 00000000-0000-4000-8000-000000000000 \
  --reality-private-key REALITY_PRIVATE_KEY \
  --reality-server-names example.com \
  --reality-short-ids '' \
  --reality-dest example.com:443
```

REALITY keys can be generated with `proxy config`, `xray x25519`, or another compatible tool. In `proxy config` interactive mode, leaving `reality_private_key` empty generates it automatically and derives the matching client `reality_public_key`. Keep the private key outside version control. `--reality-spider-x` is accepted for Xray config compatibility on the client side; successful REALITY handshakes do not need it.

Run client/server mode with Trojan:

```sh
bin/proxy server --listen 0.0.0.0:9443 --tunnel-protocol trojan --token change-me
bin/proxy client --server-addr 203.0.113.10:9443 --tunnel-protocol trojan --token change-me
```

Run Xray-compatible VMess over raw TCP:

```sh
bin/proxy server --listen 0.0.0.0:9443 --tunnel-protocol vmess --transport raw --token 00000000-0000-4000-8000-000000000000
bin/proxy client --server-addr 203.0.113.10:9443 --tunnel-protocol vmess --transport raw --token 00000000-0000-4000-8000-000000000000
```

Run Xray-compatible Trojan over raw TLS:

```sh
bin/proxy server --listen 0.0.0.0:443 --tunnel-protocol trojan --transport raw --tls-cert server.crt --tls-key server.key --token change-me
bin/proxy client --server-addr proxy.example.com:443 --tunnel-protocol trojan --transport raw --tls --tls-server-name proxy.example.com --token change-me
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

For SOCKS5, SOCKS5 UDP ASSOCIATE, HTTP CONNECT, and HTTP proxy requests, the proxy inspects the requested target. Force-upstream rules in `route.json` have the highest priority. Otherwise, TCP targets are tried directly first. If direct TCP connection fails, that target is remembered as upstream-only and later connections skip the direct attempt. Plain HTTP requests that normally have no request body require a first response byte within `direct_probe_timeout`. HTTP CONNECT and SOCKS5 CONNECT also probe after the client sends its first tunnel payload; if the direct target accepts TCP but never returns content, the proxy falls back upstream and replays that first payload. UDP targets keep the conservative rule: internal targets go direct, other targets go upstream.

## Upstream Protocol

The upstream protocol can be configured with `--upstream-protocol` or the top-level `upstream_protocol` field in `config.json`. Supported values are `socks5` and `mixed`; the default is `socks5`.

In `socks5` mode, SOCKS5 and HTTP proxy traffic with a parsed target is converted to SOCKS5 before going upstream. Unknown mixed traffic is rejected because it has no parsed target.

In `mixed` mode, HTTP proxy traffic and unknown mixed traffic are forwarded to the gateway unchanged. SOCKS5 TCP and UDP still use SOCKS5 negotiation, so the upstream mixed port must support SOCKS5.

## Client/Server Commands

Detailed protocol startkit docs are available in [docs/startkit.md](docs/startkit.md), with separate pages for `native`, `vless`, `vmess`, and `trojan`.

Running `proxy` without a subcommand defaults to local mode. If `config.json` contains top-level `"mode": "client"`, `"mode": "server"`, or `"mode": "local"`, that mode is used instead. Explicit `proxy local`, `proxy client`, and `proxy server` subcommands always take priority over the config mode.

`proxy local` forces local mode: the local mixed proxy forwards through the discovered gateway proxy, even if `config.json` sets `"mode": "client"` or `"mode": "server"`.

`proxy server` listens for the configured tunnel protocol and connects to the requested TCP or UDP target from the server side. Use `--listen` to choose the server bind address and `--token` to require authentication.

`proxy client` keeps the local mixed proxy listener, but upstream TCP and UDP traffic with a parsed target is encapsulated to the tunnel server. Use `--server-addr` for the server address and the same `--token` value used by the server.

By default, `proxy server` reads `server.json` next to the executable and `proxy client` reads `client.json` next to the executable. Passing `--config <path>` still overrides those mode defaults; passing `--config ""` disables runtime config loading.

`proxy config` generates ready-to-edit JSON config files without starting the proxy. Running it without flags starts an interactive wizard backed by the command runtime; press Enter to accept defaults, or enter values for the fields you want to adjust. Passing flags keeps non-interactive generation for scripts. By default it writes `server.json`, `client.json`, and `route.json`, shares one generated token between server/client configs, and accepts `--protocol native|vless|vmess|trojan`.

```sh
bin/proxy config
bin/proxy config --protocol native --server-addr proxy.example.com:9443
bin/proxy config --protocol vmess --server-addr proxy.example.com:9443
bin/proxy config --protocol trojan --transport raw --tls --tls-cert server.crt --tls-key server.key --tls-server-name proxy.example.com
bin/proxy config --target client --output client.json --protocol vless --server-addr proxy.example.com:9443
```

Subcommand aliases:

- `proxy local`: `proxy l`, `proxy loc`
- `proxy client`: `proxy c`, `proxy cli`
- `proxy server`: `proxy s`, `proxy srv`
- `proxy config`: `proxy cfg`, `proxy gen`
- `proxy version`: `proxy v`, `proxy ver`

The tunnel transport is selected with `--transport` or `tunnel_transport` in `config.json`:

- `raw`: direct TCP connection to the server. This is the default and has the least overhead.
- `ws`: WebSocket over HTTP/1.1. This is the most practical option behind nginx HTTP reverse proxy or common CDNs.
- `h2`: HTTP/2 bidirectional request/response stream. Without server certificates it runs as h2c; with `--tls-cert` and `--tls-key` it serves TLS HTTP/2.
- `h3`: HTTP/3 over QUIC. The server requires `--tls-cert` and `--tls-key`, and the client always uses `https`.

Raw transport can also run inside TLS: use client `--tls` and server `--tls-cert` plus `--tls-key`. This is the recommended transport/security combination for Trojan compatibility.

The tunnel protocol is selected with `--tunnel-protocol` or `tunnel_protocol` in `config.json`:

- `native`: this project's compact protocol. This is the default, supports TCP, SOCKS5 UDP relay, and tunnel multiplexing.
- `vless`: VLESS-style TCP request framing. `--token` must be a UUID.
- `trojan`: standard Trojan TCP request framing. `--token` is used as the Trojan password. For common Xray Trojan deployments, use raw transport with client `--tls` and server `--tls-cert` plus `--tls-key`.
- `vmess`: Xray-compatible VMess AEAD TCP request framing. `--token` must be a UUID and is used as the VMess user id. The compatibility target is `security: "none"` with AEAD header and Xray's default chunk stream/chunk masking options; AES-GCM, ChaCha20-Poly1305, VMess UDP, mux command, global padding, and authenticated length are not supported.

For Xray REALITY/Vision client compatibility, use `proxy client` with `--transport raw`, `--tunnel-protocol vless`, `--tunnel-security reality`, and `--flow xtls-rprx-vision`. REALITY requires `--reality-server-name`, `--reality-public-key`, and a UUID `--token`; `--reality-fingerprint` defaults to `chrome`.

For Xray REALITY/Vision server compatibility, use `proxy server` with `--transport raw`, `--tunnel-protocol vless`, `--tunnel-security reality`, and `--flow xtls-rprx-vision`. REALITY server mode requires `--reality-private-key`, `--reality-server-names`, and a fallback `--reality-dest` such as `example.com:443`. `proxy config` can auto-generate `reality_private_key` and derive the matching client `reality_public_key`. `--reality-short-ids` can restrict allowed shortIds; if omitted, the empty shortId is allowed. The Xray client must use the matching public key, server name, shortId, UUID, and flow.

Only `native` currently supports SOCKS5 UDP relay and tunnel multiplexing. `vless`, `vmess`, and `trojan` carry TCP streams over the selected transport.

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

Runtime configuration and route rules live in separate files. By default, local mode and `proxy` without a subcommand read runtime settings from `config.json`, `proxy server` reads runtime settings from `server.json`, and `proxy client` reads runtime settings from `client.json`. Route rules use `route.json` unless `--route-config <path>` is provided. Relative config paths are searched in this order: executable directory, current working directory, then `~/.config/proxy`. If no file exists, write-back uses the executable directory. Use `--route-config ""` to disable route loading and write-back.

Runtime config example:

```json
{
  "mode": "local",
  "listen_addr": "127.0.0.1:1080",
  "upstream_protocol": "socks5",
  "socks5_username": "",
  "socks5_password": "",
  "upstream_socks5_username": "",
  "upstream_socks5_password": "",
  "direct_probe_timeout": "500ms",
  "tunnel_protocol": "native",
  "tunnel_transport": "raw",
  "tunnel_security": "none",
  "tunnel_path": "/proxy",
  "tunnel_mux": true
}
```

Route config example:

```json
{
  "force_upstream": {
    "domains": ["x.com", "twitter.com"],
    "domain_regexes": ["^api\\.", "^pbs\\.twimg\\."],
    "domain_suffixes": ["x.com", "twitter.com"],
    "ips": ["8.8.8.8"],
    "ip_cidrs": ["1.1.1.0/24", "2001:4860:4860::/48"],
    "ip_ranges": ["203.0.113.0/24"]
  }
}
```

Rule behavior:

- `domains`: exact host match.
- `domain_regexes`: Go/RE2 regular expressions matched against the normalized lowercase host.
- `domain_suffixes`: matches the domain itself and its subdomains.
- `ips`: exact IP match.
- `ip_cidrs` and `ip_ranges`: CIDR prefix match. `ip_ranges` is an alias for CIDR-style ranges.

Before exit, learned direct TCP failures are merged into `route.json` or the configured `--route-config` file. Failed domain targets are appended to `domains`, and failed IP targets are appended to `ips`. If an existing exact domain, domain regex, domain suffix, exact IP, or IP CIDR/range already covers the target, nothing is added. When more than three subdomains under the same registrable domain appear in `domains`, that registrable domain is promoted to `domain_suffixes`, covered `domains` entries are removed, and `domain_suffixes` is deduplicated and sorted.

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
-c, --config <string>       JSON runtime config path; defaults by mode; empty disables runtime config loading [default: "config.json"]
--route-config <string>     JSON route config path; empty disables route loading and write-back [default: "route.json"]
--dial-timeout <duration>   upstream dial timeout [default: 5s]
--direct-probe-timeout <duration> timeout waiting for direct target response before falling back upstream [default: 500ms]
--gateway-ip <string>       gateway IP; empty means auto-detect
-p, --gateway-port <int>    gateway proxy port [default: 1080]
-l, --listen <string>       local listen address [default: "127.0.0.1:1080"]
--socks5-username <string>  local SOCKS5 username; enables username/password auth when set with username or password
--socks5-password <string>  local SOCKS5 password
--refresh-interval <duration> interval for checking local IPv4 changes; 0 disables refresh [default: 5s]
--scan-timeout <duration>   per-IP timeout when scanning local IPv4 networks [default: 250ms]
--scan-workers <int>        parallel workers used for IPv4 network scanning
--upstream-protocol <string> upstream protocol: socks5 or mixed [default: socks5]
--upstream-socks5-username <string> upstream SOCKS5 username
--upstream-socks5-password <string> upstream SOCKS5 password
-v, --verbose               enable debug logs
```

`proxy client` adds:

```text
--server-addr <string>      tunnel server address
--token <string>            shared token, VLESS/VMess UUID, or Trojan password
--tunnel-protocol <string>  tunnel protocol: native, vless, vmess, or trojan [default: native]
--tunnel-security <string>  tunnel security: none or reality [default: none]
--flow <string>             VLESS flow, for example xtls-rprx-vision
--transport <string>        tunnel transport: raw, ws, h2, or h3 [default: raw]
--tunnel-path <string>      HTTP/WebSocket tunnel path [default: /proxy]
--tls                       use TLS for ws/h2 transport
--tls-server-name <string>  TLS server name override
--tls-insecure              skip TLS certificate verification
--reality-server-name <string> REALITY serverName
--reality-fingerprint <string> REALITY uTLS fingerprint [default: chrome]
--reality-public-key <string>  REALITY publicKey
--reality-short-id <string>    REALITY shortId hex
--reality-spider-x <string>    REALITY spiderX path
--mux <bool>                enable tunnel multiplexing [default: true]
```

`proxy server` adds:

```text
--token <string>            shared token, VLESS/VMess UUID, or Trojan password
--tunnel-protocol <string>  tunnel protocol: native, vless, vmess, or trojan [default: native]
--tunnel-security <string>  tunnel security: none or reality [default: none]
--flow <string>             VLESS flow, for example xtls-rprx-vision
--transport <string>        tunnel transport: raw, ws, h2, or h3 [default: raw]
--tunnel-path <string>      HTTP/WebSocket tunnel path [default: /proxy]
--tls-cert <string>         TLS certificate file for raw/ws/h2/h3 server
--tls-key <string>          TLS private key file for raw/ws/h2/h3 server
--reality-private-key <string> REALITY privateKey
--reality-server-names <string> comma-separated REALITY serverNames
--reality-short-ids <string>   comma-separated REALITY shortIds in hex
--reality-dest <string>        REALITY fallback destination host:port
--mux <bool>                enable tunnel multiplexing [default: true]
```

## Make Targets

```sh
make build    # Build bin/proxy
make release  # Cross-compile release binaries into dist/
make test     # Run tests
make fmt      # Format Go code
make tidy     # Tidy Go modules
make run      # Run with Makefile defaults
make clean    # Remove build output and local Go cache
```

`make run` uses this repository's `config.json` and `route.json` by default and accepts overrides:

```sh
make run LISTEN=127.0.0.1:1081 GATEWAY_PORT=7890
make run GATEWAY_IP=192.168.1.1
make run CONFIG=/path/to/config.json
make run ROUTE_CONFIG=/path/to/route.json
make run UPSTREAM_PROTOCOL=mixed
make run MODE=local
make run MODE=server LISTEN=0.0.0.0:9443 TOKEN=change-me
make run MODE=client SERVER_ADDR=203.0.113.10:9443 TOKEN=change-me
make run MODE=server LISTEN=0.0.0.0:9443 TUNNEL_PROTOCOL=vless TOKEN=00000000-0000-4000-8000-000000000000
make run MODE=client SERVER_ADDR=203.0.113.10:9443 TUNNEL_PROTOCOL=vless TOKEN=00000000-0000-4000-8000-000000000000
make run MODE=server LISTEN=0.0.0.0:9443 TUNNEL_PROTOCOL=vmess TRANSPORT=raw TOKEN=00000000-0000-4000-8000-000000000000
make run MODE=client SERVER_ADDR=203.0.113.10:9443 TUNNEL_PROTOCOL=vmess TRANSPORT=raw TOKEN=00000000-0000-4000-8000-000000000000
make run MODE=server LISTEN=0.0.0.0:443 TUNNEL_PROTOCOL=trojan TRANSPORT=raw TOKEN=change-me TLS_CERT=server.crt TLS_KEY=server.key
make run MODE=client SERVER_ADDR=proxy.example.com:443 TUNNEL_PROTOCOL=trojan TRANSPORT=raw TOKEN=change-me TLS=1 TLS_SERVER_NAME=proxy.example.com
make run MODE=server LISTEN=0.0.0.0:443 TUNNEL_PROTOCOL=vless TRANSPORT=raw TUNNEL_SECURITY=reality FLOW=xtls-rprx-vision TOKEN=00000000-0000-4000-8000-000000000000 REALITY_PRIVATE_KEY=REALITY_PRIVATE_KEY REALITY_SERVER_NAMES=example.com REALITY_DEST=example.com:443
make run MODE=client SERVER_ADDR=proxy.example.com:443 TUNNEL_PROTOCOL=vless TRANSPORT=raw TUNNEL_SECURITY=reality FLOW=xtls-rprx-vision TOKEN=00000000-0000-4000-8000-000000000000 REALITY_SERVER_NAME=example.com REALITY_PUBLIC_KEY=REALITY_PUBLIC_KEY REALITY_FINGERPRINT=chrome
make run MODE=server LISTEN=127.0.0.1:9443 TRANSPORT=ws TUNNEL_PATH=/proxy TOKEN=change-me
make run MODE=client SERVER_ADDR=proxy.example.com:443 TRANSPORT=ws TUNNEL_PATH=/proxy TLS=1 TOKEN=change-me
make run MODE=client SERVER_ADDR=proxy.example.com:443 TRANSPORT=ws MUX=false TOKEN=change-me
```

`MODE=local`, `MODE=server`, and `MODE=client` are Makefile shortcuts that run `proxy local`, `proxy server`, and `proxy client`.

`make release` builds the targets listed in `RELEASE_TARGETS`, which defaults to Linux, macOS, and Windows on amd64/arm64 plus Linux arm/v7. Override `DIST_DIR` or `RELEASE_TARGETS` to change the output directory or platform list.

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
