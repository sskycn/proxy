# Proxy Protocol Startkit

Chinese version: [startkit.zh-CN.md](startkit.zh-CN.md)

This startkit explains the client/server tunnel protocols supported by this project. The default documentation language is English. Chinese translations use the `.zh-CN.md` suffix.

Protocol pages:

- [Native protocol](protocol-native.md)
- [VLESS protocol](protocol-vless.md)
- [VMess protocol](protocol-vmess.md)
- [Trojan protocol](protocol-trojan.md)

All examples use placeholders. Do not commit real server addresses, UUIDs, Trojan passwords, REALITY public/private keys, TLS private keys, or production config files.

## Generate Configs

Run without flags to start the interactive wizard:

```sh
bin/proxy config
```

Generate configs non-interactively:

```sh
bin/proxy config --protocol native --server-addr proxy.example.com:9443
bin/proxy config --protocol vless --server-addr proxy.example.com:9443
bin/proxy config --protocol vmess --server-addr proxy.example.com:9443
bin/proxy config --protocol trojan --server-addr proxy.example.com:443 --tls --tls-server-name proxy.example.com
```

By default, config generation writes three files:

- `server.json`: used by `proxy server`.
- `client.json`: used by `proxy client`.
- `route.json`: used by local/client routing rules and learned direct-failure targets.

Runtime config defaults:

- `proxy server` reads `server.json`.
- `proxy client` reads `client.json`.
- `proxy` and `proxy local` read `config.json`.
- Relative config paths are searched in this order: executable directory, current working directory, then `~/.config/proxy`.
- If no file exists, write-back uses the executable directory.
- `--config <path>` overrides the mode default.
- `--config ""` disables runtime config loading.
- `--route-config <path>` overrides the default `route.json` route file.
- `--route-config ""` disables route loading and write-back.

## Run

Server:

```sh
bin/proxy server
```

Client:

```sh
bin/proxy client
```

Override the config path:

```sh
bin/proxy server --config /etc/proxy/server.json
bin/proxy client --config /etc/proxy/client.json
```

## Shared Fields

| Field | Modes | Meaning |
| --- | --- | --- |
| `mode` | server/client/local | Runtime mode. `server` accepts tunnel connections, `client` opens the local mixed proxy and forwards through a tunnel server, and `local` discovers a gateway proxy. |
| `listen_addr` | server/client/local | Local listen address. Servers commonly use `0.0.0.0:9443`; clients commonly use `127.0.0.1:1080`. |
| `server_addr` | client | Tunnel server address in `host:port` form. |
| `token` | server/client | Authentication material. For `native` it is a shared token, for VLESS/VMess it is a UUID, and for Trojan it is the password. |
| `tunnel_protocol` | server/client | Tunnel protocol: `native`, `vless`, `vmess`, or `trojan`. |
| `tunnel_transport` | server/client | Carrier transport: `raw`, `ws`, `h2`, or `h3`. |
| `tunnel_path` | server/client | HTTP/WebSocket path for `ws`, `h2`, and `h3`; raw transport usually ignores it. |
| `tunnel_tls` | client | Whether the client uses TLS for raw/ws/h2 transport. HTTP/3 always uses QUIC/TLS. |
| `tunnel_tls_cert` | server | Server TLS certificate path. |
| `tunnel_tls_key` | server | Server TLS private key path. |
| `tunnel_tls_server_name` | client | TLS SNI and certificate verification name. |
| `tunnel_tls_insecure` | client | Skip TLS certificate verification. Use only for tests. |
| `tunnel_security` | server/client | Extra security layer. Currently used for VLESS REALITY with value `reality`. |
| `tunnel_flow` | server/client | VLESS flow, for example `xtls-rprx-vision`. |
| `tunnel_mux` | server/client | Enables this project's tunnel multiplexing. Currently supported by `native`. |
| `gateway_ip` | local | Upstream gateway IP. Leave empty to auto-discover only when the local machine has a private IPv4 address. |
| `gateway_port` | local | Gateway proxy port. Defaults to `1080`. |
| `upstream_protocol` | client/local | Upstream protocol used for parsed proxy traffic: `socks5` or `mixed`. |
| `socks5_username` | client/local | Local SOCKS5 username. Setting username or password enables username/password auth for SOCKS5 clients. |
| `socks5_password` | client/local | Local SOCKS5 password. |
| `upstream_socks5_username` | local | Username used when dialing an upstream SOCKS5 gateway. |
| `upstream_socks5_password` | local | Password used when dialing an upstream SOCKS5 gateway. |
| `direct_probe_timeout` | client/local | Timeout used by direct-first probing before falling back upstream. Default is `500ms`; JSON accepts Go duration strings such as `"500ms"`. |
| `dial_timeout` | server/client/local | TCP dial timeout for upstream, tunnel, and gateway checks. Default is `5s`. |
| `refresh_interval` | local | Interval for checking local IPv4 changes. Gateway rediscovery only runs after local IPv4 changes. `0` disables refresh. |
| `scan_timeout` | local | Per-IP timeout when scanning the local IPv4 network for a reachable gateway proxy. |
| `scan_workers` | local | Parallel workers used during local IPv4 network scanning. |
| `buffer_size` | server/client/local | Per-direction copy buffer size. Values below 4096 are raised to 4096. |
| `verbose` | server/client/local | Enables debug logs. Access logs are printed regardless of this setting. |

## Route Fields

Route fields live in `route.json`, not in `server.json`, `client.json`, or `config.json`. `proxy config` writes an empty route file by default.

| Field | Modes | Meaning |
| --- | --- | --- |
| `force_upstream.domains` | client/local | Exact normalized domain matches. Learned direct TCP failures for hostnames are added here unless another rule already covers them. |
| `force_upstream.domain_regexes` | client/local | Go/RE2 regular expressions matched against the normalized lowercase host. |
| `force_upstream.domain_suffixes` | client/local | Matches the suffix itself and all subdomains. Values are normalized, deduplicated, and sorted during write-back. |
| `force_upstream.ips` | client/local | Exact IP matches. Learned direct TCP failures for IP targets are added here unless another rule already covers them. |
| `force_upstream.ip_cidrs` | client/local | CIDR prefix matches. |
| `force_upstream.ip_ranges` | client/local | Alias for CIDR-style ranges; parsed the same way as `ip_cidrs`. |

Before exit, learned direct TCP failures are merged into `route.json` or the configured `--route-config` file. If more than three subdomains under the same registrable domain appear in `force_upstream.domains`, that registrable domain is promoted into `force_upstream.domain_suffixes` and the covered exact-domain entries are removed.

## Direct-First Fallback

For parsed TCP proxy traffic, force-upstream route rules win first. Otherwise the proxy tries a direct TCP connection before using the upstream.

- Plain HTTP requests that normally have no request body must receive a first response byte within `direct_probe_timeout`.
- HTTP CONNECT and SOCKS5 CONNECT wait for the client's first tunnel payload, send it to the direct target, and require a first response byte within `direct_probe_timeout`.
- If probing fails, the target is marked upstream-only, the first payload is replayed to the upstream path, and later connections skip the direct attempt.
- UDP uses a conservative rule: internal UDP targets go direct, other UDP targets go upstream.

## Transport Choices

| Transport | Best fit | Notes |
| --- | --- | --- |
| `raw` | Direct TCP, TLS, Trojan, REALITY | Lowest overhead. Prefer it when a plain TCP port is available. |
| `ws` | nginx HTTP reverse proxy, common CDNs | HTTP/1.1 WebSocket. The most practical option behind web infrastructure. |
| `h2` | HTTP/2 upstreams | Uses h2c without certs; uses TLS HTTP/2 when server cert/key are configured. |
| `h3` | HTTP/3/QUIC | Server requires TLS cert/key; client connects with HTTPS/QUIC. |

## Protocol Capability Matrix

| Protocol | TCP | SOCKS5 UDP relay | Tunnel mux | TLS | REALITY/Vision | Xray compatibility target |
| --- | --- | --- | --- | --- | --- | --- |
| native | yes | yes | yes | yes | no | Not applicable |
| vless | yes | no | no | yes | yes | VLESS TCP, REALITY/Vision |
| vmess | yes | no | no | yes | no | VMess AEAD TCP, security none |
| trojan | yes | no | no | recommended | no | Trojan TCP |

## Which Protocol Should I Use?

- Use `native` when both sides run this project and you want the best feature coverage.
- Use `vless` when you need VLESS or Xray REALITY/Vision compatibility.
- Use `vmess` when you need Xray VMess AEAD TCP compatibility with `security: "none"`.
- Use `trojan` when you need Trojan TCP compatibility, usually with raw TLS.
- Use `ws` transport when the server sits behind nginx or a common HTTP/CDN path.
