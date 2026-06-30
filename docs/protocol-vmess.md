# VMess Protocol

Chinese version: [protocol-vmess.zh-CN.md](protocol-vmess.zh-CN.md)

`vmess` provides Xray-compatible VMess AEAD TCP request framing. It is useful when you need to interoperate with VMess AEAD TCP, but it is not the most feature-complete protocol in this project.

## Best For

- Xray VMess AEAD TCP compatibility.
- UUID-based VMess user id.
- Peers configured with `security: "none"`.

## Capabilities

| Capability | Status |
| --- | --- |
| TCP proxying | Supported |
| SOCKS5 UDP relay | Not supported |
| Tunnel multiplexing | Not supported |
| raw/ws/h2/h3 transport | Supported |
| TLS | Supported |
| REALITY/Vision | Not supported |
| token format | UUID |
| VMess body security | `none` only |
| Server outbound targets | Public IPs only |

Supported compatibility target:

- VMess AEAD header.
- TCP command.
- `security: "none"`.
- Xray default chunk stream/chunk masking options.

Not supported:

- VMess UDP.
- AES-GCM or ChaCha20-Poly1305 body security.
- VMess mux command.
- Global padding.
- Authenticated length.

## Important Fields

| Field | Side | Meaning |
| --- | --- | --- |
| `tunnel_protocol: "vmess"` | server/client | Enables VMess AEAD TCP framing. |
| `token` | server/client | VMess user id. Must be a UUID. |
| `tunnel_transport` | server/client | Carrier transport: `raw`, `ws`, `h2`, or `h3`. |
| `tunnel_tls` | client | Enables TLS from client to server. |
| `tunnel_tls_cert` / `tunnel_tls_key` | server | TLS certificate and private key. |
| `tunnel_tls_server_name` | client | TLS SNI and certificate verification name. |

## Generate Configs

```sh
bin/tcptun config --protocol vmess --server-addr proxy.example.com:9443
```

WebSocket + TLS:

```sh
bin/tcptun config \
  --protocol vmess \
  --transport ws \
  --server-addr proxy.example.com:443 \
  --tunnel-path /tcptun \
  --tls \
  --tls-server-name proxy.example.com
```

## server.json

```json
{
  "mode": "server",
  "listen_addr": "0.0.0.0:9443",
  "token": "00000000-0000-4000-8000-000000000000",
  "tunnel_protocol": "vmess",
  "tunnel_transport": "raw",
  "tunnel_path": "/proxy"
}
```

## client.json

```json
{
  "mode": "client",
  "listen_addr": "127.0.0.1:1080",
  "server_addr": "proxy.example.com:9443",
  "token": "00000000-0000-4000-8000-000000000000",
  "tunnel_protocol": "vmess",
  "tunnel_transport": "raw",
  "tunnel_path": "/proxy",
  "upstream_protocol": "socks5"
}
```

## Xray Field Mapping

| Xray field | Project field |
| --- | --- |
| `protocol: "vmess"` | `tunnel_protocol: "vmess"` |
| `users[].id` | `token` |
| `users[].security: "none"` | The only supported VMess body security |
| `streamSettings.network: "tcp"` | `tunnel_transport: "raw"` |
| `streamSettings.network: "ws"` | `tunnel_transport: "ws"` |
| `streamSettings.wsSettings.path` | `tunnel_path` |
| `streamSettings.security: "tls"` | client `tunnel_tls: true`; server certificate and key |

## TLS Example

server:

```json
{
  "mode": "server",
  "listen_addr": "0.0.0.0:443",
  "token": "00000000-0000-4000-8000-000000000000",
  "tunnel_protocol": "vmess",
  "tunnel_transport": "raw",
  "tunnel_tls_cert": "/etc/tcptun/server.crt",
  "tunnel_tls_key": "/etc/tcptun/server.key"
}
```

client:

```json
{
  "mode": "client",
  "listen_addr": "127.0.0.1:1080",
  "server_addr": "proxy.example.com:443",
  "token": "00000000-0000-4000-8000-000000000000",
  "tunnel_protocol": "vmess",
  "tunnel_transport": "raw",
  "tunnel_tls": true,
  "tunnel_tls_server_name": "proxy.example.com"
}
```

## Run

```sh
bin/tcptun server
bin/tcptun client
```

Use `native` when you need UDP relay or tunnel mux.
