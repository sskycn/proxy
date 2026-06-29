# Proxy Protocol Startkit

Chinese version: [startkit.zh-CN.md](startkit.zh-CN.md)

This startkit explains the client/server tunnel protocols supported by this project. The default documentation language is English. Chinese translations use the `.zh-CN.md` suffix.

Protocol pages:

- [Custom protocol](protocol-custom.md)
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
bin/proxy config --protocol custom --server-addr proxy.example.com:9443
bin/proxy config --protocol vless --server-addr proxy.example.com:9443
bin/proxy config --protocol vmess --server-addr proxy.example.com:9443
bin/proxy config --protocol trojan --server-addr proxy.example.com:443 --tls --tls-server-name proxy.example.com
```

By default, config generation writes two files:

- `server.json`: used by `proxy server`.
- `client.json`: used by `proxy client`.

Runtime config defaults:

- `proxy server` reads `server.json` next to the executable.
- `proxy client` reads `client.json` next to the executable.
- `proxy` and `proxy local` read `config.json` next to the executable.
- `--config <path>` overrides the mode default.
- `--config ""` disables config loading and write-back.

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
| `token` | server/client | Authentication material. For `custom` it is a shared token, for VLESS/VMess it is a UUID, and for Trojan it is the password. |
| `tunnel_protocol` | server/client | Tunnel protocol: `custom`, `vless`, `vmess`, or `trojan`. |
| `tunnel_transport` | server/client | Carrier transport: `raw`, `ws`, `h2`, or `h3`. |
| `tunnel_path` | server/client | HTTP/WebSocket path for `ws`, `h2`, and `h3`; raw transport usually ignores it. |
| `tunnel_tls` | client | Whether the client uses TLS for raw/ws/h2 transport. HTTP/3 always uses QUIC/TLS. |
| `tunnel_tls_cert` | server | Server TLS certificate path. |
| `tunnel_tls_key` | server | Server TLS private key path. |
| `tunnel_tls_server_name` | client | TLS SNI and certificate verification name. |
| `tunnel_tls_insecure` | client | Skip TLS certificate verification. Use only for tests. |
| `tunnel_security` | server/client | Extra security layer. Currently used for VLESS REALITY with value `reality`. |
| `tunnel_flow` | server/client | VLESS flow, for example `xtls-rprx-vision`. |
| `tunnel_mux` | server/client | Enables this project's tunnel multiplexing. Currently supported by `custom`. |
| `upstream_protocol` | client/local | Upstream protocol used for parsed proxy traffic: `socks5` or `mixed`. |
| `force_upstream` | client/local | Force-upstream routing rules by domain, prefix, suffix, IP, CIDR, or IP range. |

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
| custom | yes | yes | yes | yes | no | Not applicable |
| vless | yes | no | no | yes | yes | VLESS TCP, REALITY/Vision |
| vmess | yes | no | no | yes | no | VMess AEAD TCP, security none |
| trojan | yes | no | no | recommended | no | Trojan TCP |

## Which Protocol Should I Use?

- Use `custom` when both sides run this project and you want the best feature coverage.
- Use `vless` when you need VLESS or Xray REALITY/Vision compatibility.
- Use `vmess` when you need Xray VMess AEAD TCP compatibility with `security: "none"`.
- Use `trojan` when you need Trojan TCP compatibility, usually with raw TLS.
- Use `ws` transport when the server sits behind nginx or a common HTTP/CDN path.
