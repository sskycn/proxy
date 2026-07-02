# VLESS Protocol

Chinese version: [protocol-vless.zh-CN.md](protocol-vless.zh-CN.md)

`vless` provides VLESS-style TCP and UDP request framing. This project supports regular VLESS TCP/UDP and Xray-compatible REALITY/Vision mode.

## Best For

- Xray VLESS TCP/UDP compatibility.
- REALITY/Vision deployments.
- UUID-based user identity matching Xray `users[].id`.

## Capabilities

| Capability | Status |
| --- | --- |
| TCP proxying | Supported |
| SOCKS5 UDP relay | Supported |
| Tunnel multiplexing | Not supported |
| raw/ws/h2/h3 transport | Supported |
| TLS | Supported |
| REALITY/Vision | Supported, raw transport only |
| token format | UUID |
| Server outbound targets | Public IPs only |

## Important Fields

| Field | Side | Meaning |
| --- | --- | --- |
| `tunnel_protocol: "vless"` | server/client | Enables VLESS framing. |
| `token` | server/client | VLESS user id. Must be a UUID. |
| `tunnel_transport` | server/client | Carrier transport. REALITY/Vision requires `raw`. |
| `tunnel_security: "reality"` | server/client | Enables REALITY. Leave empty or `none` for regular VLESS. |
| `tunnel_flow` | server/client | VLESS flow, usually `xtls-rprx-vision` for Vision. |
| `reality_server_name` | client | REALITY serverName sent by the client. |
| `reality_server_names` | server | Allowed REALITY serverName values. |
| `reality_public_key` | client | REALITY public key. `tcptun config` derives it from `reality_private_key` when both sides are generated together. |
| `reality_private_key` | server | REALITY private key. In interactive `tcptun config`, leave it empty to generate one automatically. |
| `reality_short_id` | client | Client shortId in hex, may be empty. |
| `reality_short_ids` | server | Allowed shortId list, may include the empty value. |
| `reality_fingerprint` | client | uTLS fingerprint, for example `chrome`. |
| `reality_dest` | server | REALITY fallback destination in `host:port` form. |
| `reality_spider_x` | client | Xray-compatible spiderX field, commonly `/`. |

## Regular VLESS

Generate:

```sh
bin/tcptun config --protocol vless --server-addr proxy.example.com:9443
```

server:

```json
{
  "mode": "server",
  "listen_addrs": ["0.0.0.0:9443"],
  "token": "00000000-0000-4000-8000-000000000000",
  "tunnel_protocol": "vless",
  "tunnel_transport": "raw",
  "tunnel_path": "/proxy"
}
```

client:

```json
{
  "mode": "client",
  "listen_addrs": ["127.0.0.1:1080"],
  "server_addr": "proxy.example.com:9443",
  "token": "00000000-0000-4000-8000-000000000000",
  "tunnel_protocol": "vless",
  "tunnel_transport": "raw",
  "tunnel_path": "/proxy",
  "upstream_protocol": "socks5"
}
```

## VLESS REALITY/Vision

Requirements:

- `tunnel_protocol` is `vless`.
- `tunnel_transport` is `raw`.
- `tunnel_security` is `reality`.
- `tunnel_tls` is disabled.
- `token` is a UUID.
- Client and server REALITY key, serverName, shortId, and flow settings match.

Generate:

```sh
bin/tcptun config \
  --protocol vless \
  --transport raw \
  --tunnel-security reality \
  --flow xtls-rprx-vision \
  --server-addr proxy.example.com:443
```

When this command runs in interactive mode, an empty `reality_private_key` generates a new X25519 key pair. The generated `server.json` receives `reality_private_key`, and the generated `client.json` receives the matching `reality_public_key`.

server:

```json
{
  "mode": "server",
  "listen_addrs": ["0.0.0.0:443"],
  "token": "00000000-0000-4000-8000-000000000000",
  "tunnel_protocol": "vless",
  "tunnel_transport": "raw",
  "tunnel_security": "reality",
  "tunnel_flow": "xtls-rprx-vision",
  "reality_private_key": "REALITY_PRIVATE_KEY",
  "reality_server_names": ["example.com"],
  "reality_short_ids": [""],
  "reality_dest": "example.com:443"
}
```

client:

```json
{
  "mode": "client",
  "listen_addrs": ["127.0.0.1:1080"],
  "server_addr": "proxy.example.com:443",
  "token": "00000000-0000-4000-8000-000000000000",
  "tunnel_protocol": "vless",
  "tunnel_transport": "raw",
  "tunnel_security": "reality",
  "tunnel_flow": "xtls-rprx-vision",
  "reality_server_name": "example.com",
  "reality_fingerprint": "chrome",
  "reality_public_key": "REALITY_PUBLIC_KEY",
  "reality_short_id": "",
  "reality_spider_x": "/",
  "upstream_protocol": "socks5"
}
```

## Xray Field Mapping

| Xray field | Project field |
| --- | --- |
| `protocol: "vless"` | `tunnel_protocol: "vless"` |
| `users[].id` | `token` |
| `users[].flow` | `tunnel_flow` |
| `streamSettings.network: "tcp"` | `tunnel_transport: "raw"` |
| `streamSettings.security: "reality"` | `tunnel_security: "reality"` |
| `realitySettings.serverName` | client `reality_server_name` |
| `realitySettings.serverNames` | server `reality_server_names` |
| `realitySettings.publicKey` | client `reality_public_key` |
| `realitySettings.privateKey` | server `reality_private_key` |
| `realitySettings.shortId` | client `reality_short_id` |
| `realitySettings.shortIds` | server `reality_short_ids` |
| `realitySettings.dest` | server `reality_dest` |
| `realitySettings.fingerprint` | client `reality_fingerprint` |
| `realitySettings.spiderX` | client `reality_spider_x` |

## Run

```sh
bin/tcptun server
bin/tcptun client
```

VLESS supports UDP relay by opening one VLESS UDP request per UDP target when mux is disabled. When `tunnel_mux` is enabled, VLESS uses Xray-compatible mux.cool frames on one shared VLESS mux command connection, so tcptun can interoperate with Xray mux and with another tcptun endpoint.
