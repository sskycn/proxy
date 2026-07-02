# Native Protocol

Chinese version: [protocol-native.zh-CN.md](protocol-native.zh-CN.md)

`native` is this project's native tunnel protocol and the default protocol. It is designed for project-owned client/server deployments where low overhead and full feature coverage matter more than compatibility with external tcptun stacks.

## Best For

- Both client and server run this project.
- You need TCP, SOCKS5 UDP relay, and tunnel multiplexing.
- You want fewer transport handshakes over WebSocket, HTTP/2, or HTTP/3.
- You do not need Xray, V2Ray, or Trojan compatibility.

## Capabilities

| Capability | Status |
| --- | --- |
| TCP proxying | Supported |
| SOCKS5 UDP relay | Supported |
| Tunnel multiplexing | Supported |
| raw/ws/h2/h3 transport | Supported |
| TLS | Supported |
| REALITY/Vision | Not supported |
| External Xray compatibility | Not applicable |
| Server outbound targets | Public IPs only |

## Important Fields

| Field | Side | Meaning |
| --- | --- | --- |
| `tunnel_protocol: "native"` | server/client | Enables the native protocol. |
| `token` | server/client | Shared authentication token. Production deployments should always set it. |
| `tunnel_transport` | server/client | Carrier transport. `raw` is the default and has the lowest overhead. |
| `tunnel_mux` | server/client | Enables multiplexing. Recommended for tcptun-to-tcptun deployments. |
| `tunnel_path` | server/client | Path used by WebSocket/HTTP transports. Raw transport can keep the default. |
| `tunnel_tls` | client | Enables TLS from client to server. |
| `tunnel_tls_cert` / `tunnel_tls_key` | server | TLS certificate and private key for the server. |

## Generate Configs

Interactive:

```sh
bin/tcptun config
```

Non-interactive:

```sh
bin/tcptun config --protocol native --server-addr proxy.example.com:9443
```

WebSocket over TLS:

```sh
bin/tcptun config \
  --protocol native \
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
  "listen_addrs": ["0.0.0.0:9443"],
  "token": "CHANGE_ME_RANDOM_TOKEN",
  "tunnel_protocol": "native",
  "tunnel_transport": "raw",
  "tunnel_path": "/proxy",
  "tunnel_mux": true
}
```

## client.json

```json
{
  "mode": "client",
  "listen_addrs": ["127.0.0.1:1080"],
  "server_addr": "proxy.example.com:9443",
  "token": "CHANGE_ME_RANDOM_TOKEN",
  "tunnel_protocol": "native",
  "tunnel_transport": "raw",
  "tunnel_path": "/proxy",
  "tunnel_mux": true,
  "upstream_protocol": "socks5"
}
```

## TLS Example

Server:

```json
{
  "mode": "server",
  "listen_addrs": ["0.0.0.0:443"],
  "token": "CHANGE_ME_RANDOM_TOKEN",
  "tunnel_protocol": "native",
  "tunnel_transport": "raw",
  "tunnel_tls_cert": "/etc/tcptun/server.crt",
  "tunnel_tls_key": "/etc/tcptun/server.key"
}
```

Client:

```json
{
  "mode": "client",
  "listen_addrs": ["127.0.0.1:1080"],
  "server_addr": "proxy.example.com:443",
  "token": "CHANGE_ME_RANDOM_TOKEN",
  "tunnel_protocol": "native",
  "tunnel_transport": "raw",
  "tunnel_tls": true,
  "tunnel_tls_server_name": "proxy.example.com"
}
```

## Multiplexing

`native` supports tunnel multiplexing with the built-in mux command. With mux enabled, the client keeps one shared tunnel transport connection and opens one logical stream per proxied TCP connection or UDP relay. `vless` and `vmess` use Xray-compatible mux.cool frames, while `trojan` uses the tcptun-private mux layer when both ends are tcptun.

Recommendations:

- Keep mux enabled for WebSocket, HTTP/2, and HTTP/3 to reduce repeated handshakes.
- Keep mux enabled for stable raw TCP deployments when you want fewer connections.
- Set `"tunnel_mux": false` temporarily when diagnosing middlebox or long-lived connection issues.

## Run

```sh
bin/tcptun server
bin/tcptun client
```

The default local tcptun address is:

```text
127.0.0.1:1080
```
