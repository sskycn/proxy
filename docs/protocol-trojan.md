# Trojan Protocol

Chinese version: [protocol-trojan.zh-CN.md](protocol-trojan.zh-CN.md)

`trojan` provides standard Trojan TCP request framing. Common Trojan deployments use raw transport over TLS, usually on port `443`.

## Best For

- Trojan TCP compatibility.
- Password-based authentication.
- TLS entrypoints such as `443`.

## Capabilities

| Capability | Status |
| --- | --- |
| TCP proxying | Supported |
| SOCKS5 UDP relay | Not supported |
| Tunnel multiplexing | Not supported |
| raw/ws/h2/h3 transport | Supported |
| TLS | Strongly recommended |
| REALITY/Vision | Not supported |
| token format | Trojan password |

## Important Fields

| Field | Side | Meaning |
| --- | --- | --- |
| `tunnel_protocol: "trojan"` | server/client | Enables Trojan TCP framing. |
| `token` | server/client | Trojan password. The wire protocol uses the password hash. |
| `tunnel_transport` | server/client | `raw` is recommended. Other project transports can also carry Trojan framing. |
| `tunnel_tls` | client | Enables TLS from client to server. Recommended for Trojan deployments. |
| `tunnel_tls_cert` / `tunnel_tls_key` | server | Server TLS certificate and private key. |
| `tunnel_tls_server_name` | client | TLS SNI and certificate verification name. |
| `tunnel_tls_insecure` | client | Skips TLS verification. Use only for testing. |

## Generate Configs

```sh
bin/proxy config \
  --protocol trojan \
  --transport raw \
  --server-addr proxy.example.com:443 \
  --tls \
  --tls-server-name proxy.example.com
```

Include certificate paths:

```sh
bin/proxy config \
  --protocol trojan \
  --transport raw \
  --server-addr proxy.example.com:443 \
  --tls \
  --tls-cert /etc/proxy/server.crt \
  --tls-key /etc/proxy/server.key \
  --tls-server-name proxy.example.com
```

## server.json

```json
{
  "mode": "server",
  "listen_addr": "0.0.0.0:443",
  "token": "CHANGE_ME_TROJAN_PASSWORD",
  "tunnel_protocol": "trojan",
  "tunnel_transport": "raw",
  "tunnel_path": "/proxy",
  "tunnel_tls_cert": "/etc/proxy/server.crt",
  "tunnel_tls_key": "/etc/proxy/server.key",
  "force_upstream": {
    "domains": [],
    "domain_prefixes": [],
    "domain_suffixes": [],
    "ip_cidrs": [],
    "ip_ranges": [],
    "ips": []
  }
}
```

## client.json

```json
{
  "mode": "client",
  "listen_addr": "127.0.0.1:1080",
  "server_addr": "proxy.example.com:443",
  "token": "CHANGE_ME_TROJAN_PASSWORD",
  "tunnel_protocol": "trojan",
  "tunnel_transport": "raw",
  "tunnel_path": "/proxy",
  "tunnel_tls": true,
  "tunnel_tls_server_name": "proxy.example.com",
  "upstream_protocol": "socks5",
  "force_upstream": {
    "domains": [],
    "domain_prefixes": [],
    "domain_suffixes": [],
    "ip_cidrs": [],
    "ip_ranges": [],
    "ips": []
  }
}
```

## Xray Trojan Field Mapping

| Xray field | Project field |
| --- | --- |
| `protocol: "trojan"` | `tunnel_protocol: "trojan"` |
| `servers[].password` or `clients[].password` | `token` |
| `streamSettings.network: "tcp"` | `tunnel_transport: "raw"` |
| `streamSettings.security: "tls"` | client `tunnel_tls: true`; server certificate and key |
| `tlsSettings.serverName` | `tunnel_tls_server_name` |

## TLS Notes

- Do not use `tunnel_tls_insecure: true` in production.
- `tunnel_tls_server_name` should match the certificate DNS name.
- `server_addr` may be an IP or domain. If the certificate is issued for a domain, still set `tunnel_tls_server_name` to that domain.
- TLS private key paths belong only in server config.

## Run

```sh
bin/proxy server
bin/proxy client
```

Trojan currently carries TCP only. Use `custom` when you need UDP relay or tunnel mux.
