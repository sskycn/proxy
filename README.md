# tcptun

Local mixed proxy forwarder and TCP tunnel written in Go.

This tool opens a local mixed proxy port and forwards upstream traffic through the gateway. The upstream protocol is configurable and defaults to SOCKS5. It is designed for low overhead: connection forwarding uses pooled copy buffers, while HTTP and SOCKS5 requests are parsed only enough to choose direct or upstream routing.

English | [简体中文](README.zh-CN.md)

## Features

- Listens locally on `127.0.0.1:1080` by default.
- Forwards to the gateway proxy port `1080` by default.
- Accepts mixed local proxy traffic such as SOCKS5, HTTP proxy, and HTTP CONNECT.
- Supports username/password authentication for the local SOCKS5 listener.
- Uses SOCKS5 for upstream traffic by default; `mixed` upstream mode is also supported.
- Supports username/password authentication when dialing an upstream SOCKS5 gateway.
- Supports `tcptun`, `tcptun local`, `tcptun client`, and `tcptun server` commands with configurable tunnel protocols: `native`, `vless`, `vmess`, and `trojan`.
- Carries the client/server tunnel over raw TCP, WebSocket, HTTP/2, or HTTP/3 transport.
- Multiplexes client/server tunnel streams by default, so many TCP connections and UDP relays can share one upstream tunnel transport connection.
- Supports SOCKS5 UDP ASSOCIATE for UDP relay traffic.
- Prints compact terminal access logs; direct connections omit the upstream field.
- Auto-detects the default gateway IP.
- Checks whether the detected gateway port is reachable only when the machine has an internal IPv4 address.
- Scans internal local IPv4 networks when the detected gateway is unreachable and keeps all reachable proxy candidates.
- Prefers faster scanned upstreams while keeping the same source IP bound to the same upstream until that upstream fails.
- Periodically refreshes reachable upstreams so new connections follow network changes.
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
bin/tcptun
```

You can also build directly with Go:

```sh
go build -trimpath -ldflags "-s -w" -o bin/tcptun ./cmd/tcptun
```

## Run

Start with automatic gateway discovery:

```sh
make run
```

Or run the built binary:

```sh
bin/tcptun
```

Use a different local port:

```sh
bin/tcptun --listen 127.0.0.1:1081
```

Use a known gateway IP:

```sh
bin/tcptun --gateway-ip 192.168.1.1
```

Use a different gateway proxy port:

```sh
bin/tcptun --gateway-port 7890
```

Use a mixed upstream gateway instead of the default SOCKS5 upstream:

```sh
bin/tcptun --upstream-protocol mixed
```

Run explicitly in local mode, ignoring any `mode` value from `config.json`:

```sh
bin/tcptun local
```

Use a different runtime config:

```sh
bin/tcptun --config ./config.json
```

Use a different route config:

```sh
bin/tcptun --route-config ./route.json
```

Run as a tunnel server:

```sh
bin/tcptun server --listen 0.0.0.0:9443 --token change-me
```

Run as a tunnel client:

```sh
bin/tcptun client --listen 127.0.0.1:1080 --server-addr 203.0.113.10:9443 --token change-me
```

Run through an HTTP reverse proxy or CDN with WebSocket:

```sh
bin/tcptun server --listen 127.0.0.1:9443 --transport ws --tunnel-path /tcptun --token change-me
bin/tcptun client --listen 127.0.0.1:1080 --server-addr proxy.example.com:443 --transport ws --tunnel-path /tcptun --tls --token change-me
```

Run client/server mode with VLESS:

```sh
bin/tcptun server --listen 0.0.0.0:9443 --tunnel-protocol vless --token 00000000-0000-4000-8000-000000000000
bin/tcptun client --server-addr 203.0.113.10:9443 --tunnel-protocol vless --token 00000000-0000-4000-8000-000000000000
```

Connect to an Xray VLESS REALITY/Vision server. The values below are placeholders; do not commit real server addresses, UUIDs, public keys, or private keys to documentation:

```sh
bin/tcptun client \
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
bin/tcptun server \
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

REALITY keys can be generated with `tcptun config`, `xray x25519`, or another compatible tool. In `tcptun config` interactive mode, leaving `reality_private_key` empty generates it automatically and derives the matching client `reality_public_key`. Keep the private key outside version control. `--reality-spider-x` is accepted for Xray config compatibility on the client side; successful REALITY handshakes do not need it.

Run client/server mode with Trojan:

```sh
bin/tcptun server --listen 0.0.0.0:9443 --tunnel-protocol trojan --token change-me
bin/tcptun client --server-addr 203.0.113.10:9443 --tunnel-protocol trojan --token change-me
```

Run Xray-compatible VMess over raw TCP:

```sh
bin/tcptun server --listen 0.0.0.0:9443 --tunnel-protocol vmess --transport raw --token 00000000-0000-4000-8000-000000000000
bin/tcptun client --server-addr 203.0.113.10:9443 --tunnel-protocol vmess --transport raw --token 00000000-0000-4000-8000-000000000000
```

Run Xray-compatible Trojan over raw TLS:

```sh
bin/tcptun server --listen 0.0.0.0:443 --tunnel-protocol trojan --transport raw --tls-cert server.crt --tls-key server.key --token change-me
bin/tcptun client --server-addr proxy.example.com:443 --tunnel-protocol trojan --transport raw --tls --tls-server-name proxy.example.com --token change-me
```

Run over HTTP/2:

```sh
bin/tcptun server --listen 127.0.0.1:9443 --transport h2 --tunnel-path /tcptun --token change-me
bin/tcptun client --server-addr 127.0.0.1:9443 --transport h2 --tunnel-path /tcptun --token change-me
```

Run over HTTP/3 with TLS certificates:

```sh
bin/tcptun server --listen 0.0.0.0:9443 --transport h3 --tunnel-path /tcptun --tls-cert server.crt --tls-key server.key --token change-me
bin/tcptun client --server-addr proxy.example.com:9443 --transport h3 --tunnel-path /tcptun --token change-me
```

## Gateway Discovery

When `--gateway-ip` is not set, startup works like this:

1. Check whether the machine has an internal IPv4 address.
2. If it does, detect the system default gateway IP.
3. Try to connect to `<gateway-ip>:<gateway-port>`.
4. If that connection fails, scan internal local IPv4 networks for hosts with `<gateway-port>` open.
5. If no host is found, pause for `--scan-retry-interval` and scan again.
6. Keep all reachable scanned hosts as upstream candidates, sorted by measured connection latency.

If the machine has no internal IPv4 address, automatic gateway probing and local IPv4 scanning are skipped. Set `--gateway-ip` explicitly in that case.

Manual `--gateway-ip` disables scanning and uses the provided IP directly.

While running, the tcptun checks local IPv4 addresses every `--refresh-interval`. Gateway discovery and local-network scanning only run when the local IPv4 address set changes. Existing connections continue on their current upstream; new connections use the refreshed targets after a change is detected.

When multiple upstream candidates are available from scanning, new sources prefer the fastest currently known upstream. The same source IP keeps using the same upstream so long as it remains usable; if that upstream fails to connect or complete its upstream protocol handshake, the binding is cleared and the next best candidate is tried.

## Internal Address Bypass

For SOCKS5, SOCKS5 UDP ASSOCIATE, HTTP CONNECT, and HTTP proxy requests, the tcptun inspects the requested target. Force-upstream rules in `route.json` have the highest priority. Otherwise, TCP targets are tried directly first. If direct TCP connection fails, that target is remembered as upstream-only and later connections skip the direct attempt. Plain HTTP requests that normally have no request body require a first response byte within `direct_probe_timeout`. HTTP CONNECT and SOCKS5 CONNECT also probe after the client sends its first tunnel payload; if the direct target accepts TCP but never returns content, the tcptun falls back upstream and replays that first payload. UDP targets keep the conservative rule: internal targets go direct, other targets go upstream.

## Upstream Protocol

The upstream protocol can be configured with `--upstream-protocol` or the top-level `upstream_protocol` field in `config.json`. Supported values are `socks5` and `mixed`; the default is `socks5`.

In `socks5` mode, SOCKS5 and HTTP proxy traffic with a parsed target is converted to SOCKS5 before going upstream. Unknown mixed traffic is rejected because it has no parsed target.

In `mixed` mode, HTTP proxy traffic and unknown mixed traffic are forwarded to the gateway unchanged. SOCKS5 TCP and UDP still use SOCKS5 negotiation, so the upstream mixed port must support SOCKS5.

## Client/Server Commands

Detailed protocol startkit docs are available in [docs/startkit.md](docs/startkit.md), with separate pages for `native`, `vless`, `vmess`, and `trojan`.

Running `tcptun` without a subcommand defaults to local mode. If `config.json` contains top-level `"mode": "client"`, `"mode": "server"`, or `"mode": "local"`, that mode is used instead. Explicit `tcptun local`, `tcptun client`, and `tcptun server` subcommands always take priority over the config mode.

`tcptun local` forces local mode: the local mixed proxy listener forwards through the discovered gateway, even if `config.json` sets `"mode": "client"` or `"mode": "server"`.

`tcptun server` listens for the configured tunnel protocol and connects to requested targets from the server side. Server-side outbound targets must resolve to public IP addresses; private, loopback, link-local, multicast, CGNAT, and reserved ranges are rejected before dialing. TCP and SOCKS5 UDP relay are supported by every tunnel protocol. Use `--listen addr1,addr2` or JSON `listen_addrs` to bind one server to multiple local addresses.

`tcptun client` keeps the local mixed proxy listener, but upstream traffic with a parsed target is encapsulated to the tunnel server. Use `--server-addr` for the server address and the same `--token` value used by the server.

By default, `tcptun server` reads `server.json` next to the executable and `tcptun client` reads `client.json` next to the executable. Passing `--config <path>` still overrides those mode defaults; passing `--config ""` disables runtime config loading.

`tcptun config` generates ready-to-edit JSON config files without starting a listener. Running it without flags starts an interactive wizard backed by the command runtime; press Enter to accept defaults, or enter values for the fields you want to adjust. Passing generation flags keeps non-interactive generation for scripts. By default it writes `server.json`, `client.json`, and `route.json`, shares one generated token between server/client configs, and accepts `--protocol native|vless|vmess|trojan`.

```sh
bin/tcptun config
bin/tcptun config --protocol native --server-addr proxy.example.com:9443
bin/tcptun config --protocol vmess --server-addr proxy.example.com:9443
bin/tcptun config --protocol trojan --transport raw --tls --tls-cert server.crt --tls-key server.key --tls-server-name proxy.example.com
bin/tcptun config --target client --output client.json --protocol vless --server-addr proxy.example.com:9443
```

Config generation flags:

```text
--target <both|server|client>     files to generate [default: both]
--protocol <native|vless|vmess|trojan>
--transport <raw|ws|h2|h3>
--token <string>                  generated when empty; UUID for VLESS/VMess
--out-dir <path>                  output directory [default: .]
--server-output <path>            server config output [default: server.json]
--client-output <path>            client config output [default: client.json]
--route-output <path>             route config output [default: route.json]
-o, --output <path>               single output path with --target server/client
--server-listen <addr>            server listen address [default: 0.0.0.0:9443]
--client-listen <addr>            client listen address [default: 127.0.0.1:1080]
--server-addr <addr>              server address written to client config
--tunnel-path <path>              HTTP/WebSocket tunnel path [default: /proxy]
--tls, --tls-cert, --tls-key, --tls-server-name, --tls-insecure
--tunnel-security <none|reality>, --flow <string>
--reality-server-name, --reality-server-names, --reality-fingerprint
--reality-public-key, --reality-private-key, --reality-short-id, --reality-short-ids
--reality-dest, --reality-spider-x
--mux <true|false>
--client-upstream-protocol <socks5|mixed>
--client-socks5-username, --client-socks5-password
--client-upstream-socks5-username, --client-upstream-socks5-password
--force-ip-cidrs <cidr,cidr>      initial force-upstream CIDRs in route config
--overwrite                       replace existing generated files
```

Subcommand aliases:

- `tcptun local`: `tcptun l`, `tcptun loc`
- `tcptun client`: `tcptun c`, `tcptun cli`
- `tcptun server`: `tcptun s`, `tcptun srv`
- `tcptun config`: `tcptun cfg`, `tcptun gen`
- `tcptun version`: `tcptun v`, `tcptun ver`

The tunnel transport is selected with `--transport` or `tunnel_transport` in `config.json`:

- `raw`: direct TCP connection to the server. This is the default and has the least overhead.
- `ws`: WebSocket over HTTP/1.1. This is the most practical option behind nginx HTTP reverse proxy or common CDNs.
- `h2`: HTTP/2 bidirectional request/response stream. Without server certificates it runs as h2c; with `--tls-cert` and `--tls-key` it serves TLS HTTP/2.
- `h3`: HTTP/3 over QUIC. The server requires `--tls-cert` and `--tls-key`, and the client always uses `https`.

Raw transport can also run inside TLS: use client `--tls` and server `--tls-cert` plus `--tls-key`. This is the recommended transport/security combination for Trojan compatibility.

The tunnel protocol is selected with `--tunnel-protocol` or `tunnel_protocol` in `config.json`:

- `native`: this project's compact protocol. This is the default, supports TCP, SOCKS5 UDP relay, and tunnel multiplexing.
- `vless`: VLESS-style TCP and UDP request framing. `--token` must be a UUID.
- `trojan`: standard Trojan TCP and UDP request framing. `--token` is used as the Trojan password. For common Xray Trojan deployments, use raw transport with client `--tls` and server `--tls-cert` plus `--tls-key`.
- `vmess`: Xray-compatible VMess AEAD TCP and UDP request framing. `--token` must be a UUID and is used as the VMess user id. The compatibility target is `security: "none"` with AEAD header and Xray's default chunk stream/chunk masking options; AES-GCM, ChaCha20-Poly1305, VMess mux command, global padding, and authenticated length are not supported.

For Xray REALITY/Vision client compatibility, use `tcptun client` with `--transport raw`, `--tunnel-protocol vless`, `--tunnel-security reality`, and `--flow xtls-rprx-vision`. REALITY requires `--reality-server-name`, `--reality-public-key`, and a UUID `--token`; `--reality-fingerprint` defaults to `chrome`.

For Xray REALITY/Vision server compatibility, use `tcptun server` with `--transport raw`, `--tunnel-protocol vless`, `--tunnel-security reality`, and `--flow xtls-rprx-vision`. REALITY server mode requires `--reality-private-key`, `--reality-server-names`, and a fallback `--reality-dest` such as `example.com:443`. `tcptun config` can auto-generate `reality_private_key` and derive the matching client `reality_public_key`. `--reality-short-ids` can restrict allowed shortIds; if omitted, the empty shortId is allowed. The Xray client must use the matching public key, server name, shortId, UUID, and flow.

All tunnel protocols support SOCKS5 UDP relay in client/server mode. Only `native` supports tunnel multiplexing; `vless`, `vmess`, and `trojan` open one compatibility UDP request per UDP target.

Tunnel multiplexing is enabled by default for the `native` protocol. With multiplexing enabled, `tcptun client` keeps a shared tunnel transport connection to `tcptun server`, then opens one logical stream for each proxied TCP connection or UDP relay. This reduces WebSocket/HTTP/2/HTTP/3 handshakes and works better behind HTTP/CDN infrastructure. Use `--mux=false` or `"tunnel_mux": false` to fall back to one tunnel transport connection per proxied stream.

### nginx WebSocket Example

For nginx HTTP reverse proxy, run the server on loopback:

```sh
bin/tcptun server --listen 127.0.0.1:9443 --transport ws --tunnel-path /tcptun --token change-me
```

Then configure a WebSocket location:

```nginx
location /tcptun {
    proxy_pass http://127.0.0.1:9443;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
}
```

The client should connect to the public HTTPS name:

```sh
bin/tcptun client --server-addr proxy.example.com:443 --transport ws --tunnel-path /tcptun --tls --token change-me
```

## Route Config

Runtime configuration and route rules live in separate files. By default, local mode and `tcptun` without a subcommand read runtime settings from `config.json`, `tcptun server` reads runtime settings from `server.json`, and `tcptun client` reads runtime settings from `client.json`. Route rules use `route.json` unless `--route-config <path>` is provided. Relative config paths are searched in this order: executable directory, current working directory, then `~/.config/tcptun`. If no file exists, write-back uses the executable directory. Use `--route-config ""` to disable route loading and write-back.

Runtime JSON files load the fields shown below plus the client/server tunnel and REALITY fields. Operational knobs such as `gateway_ip`, `gateway_port`, `dial_timeout`, `refresh_interval`, `scan_timeout`, `scan_workers`, `buffer_size`, and `verbose` are command-line or Go API settings; they are not loaded from JSON runtime config files.

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
  "scan_retry_interval": "5s",
  "tunnel_protocol": "native",
  "tunnel_transport": "raw",
  "tunnel_security": "none",
  "tunnel_path": "/proxy",
  "tunnel_mux": true
}
```

Server configs may use `listen_addrs` instead of `listen_addr` when the same server should bind multiple addresses:

```json
{
  "mode": "server",
  "listen_addrs": ["0.0.0.0:443", "[::]:443"],
  "token": "change-me",
  "tunnel_protocol": "vless",
  "tunnel_transport": "raw"
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

UDP is supported through SOCKS5 UDP ASSOCIATE. The TCP mixed proxy port negotiates a UDP relay address, then UDP datagrams use the standard SOCKS5 UDP packet header. Internal UDP targets are sent directly from the local relay; non-internal UDP targets are relayed through the upstream gateway or, in client/server mode, through the selected tunnel protocol. The server still enforces public-IP-only outbound access and drops UDP datagrams whose target is internal or otherwise non-public.

## Access Logs

The tcptun prints one access line for each routed TCP connection and SOCKS5 UDP datagram. The source includes the detected proxy protocol and a friendly local address; HTTP CONNECT is logged as `httpc`, while normal HTTP proxy traffic is logged as `http`. Proxied traffic includes the upstream address; direct traffic omits that middle field. The final field is `ok` or the failure reason.

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

`tcptun client` adds:

```text
--server-addr <string>      tunnel server address
--token <string>            shared token, VLESS/VMess UUID, or Trojan password
--tunnel-protocol <string>  tunnel protocol: native, vless, vmess, or trojan [default: native]
--tunnel-security <string>  tunnel security: none or reality [default: none]
--flow <string>             VLESS flow, for example xtls-rprx-vision
--transport <string>        tunnel transport: raw, ws, h2, or h3 [default: raw]
--tunnel-path <string>      HTTP/WebSocket tunnel path [default: /proxy]
--tls                       use TLS for raw/ws/h2 transport
--tls-server-name <string>  TLS server name override
--tls-insecure              skip TLS certificate verification
--reality-server-name <string> REALITY serverName
--reality-fingerprint <string> REALITY uTLS fingerprint [default: chrome]
--reality-public-key <string>  REALITY publicKey
--reality-short-id <string>    REALITY shortId hex
--reality-spider-x <string>    REALITY spiderX path
--mux <bool>                enable tunnel multiplexing [default: true]
```

`tcptun server` adds:

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
make build    # Build bin/tcptun
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
make run MODE=server LISTEN=127.0.0.1:9443 TRANSPORT=ws TUNNEL_PATH=/tcptun TOKEN=change-me
make run MODE=client SERVER_ADDR=proxy.example.com:443 TRANSPORT=ws TUNNEL_PATH=/tcptun TLS=1 TOKEN=change-me
make run MODE=client SERVER_ADDR=proxy.example.com:443 TRANSPORT=ws MUX=false TOKEN=change-me
```

`MODE=local`, `MODE=server`, and `MODE=client` are Makefile shortcuts that run `tcptun local`, `tcptun server`, and `tcptun client`.

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
