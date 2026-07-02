# VLESS 协议配置

English version: [protocol-vless.md](protocol-vless.md)

`vless` 用于 VLESS 风格的 TCP 和 UDP 请求封装。本项目支持普通 VLESS TCP/UDP，也支持 Xray REALITY/Vision 兼容模式。

## 适用场景

- 需要和 Xray VLESS TCP/UDP 配置兼容。
- 需要使用 REALITY/Vision。
- 希望 token 使用 UUID 形式，便于和 Xray 用户 id 对齐。

## 能力和限制

| 能力 | 状态 |
| --- | --- |
| TCP 代理 | 支持 |
| SOCKS5 UDP relay | 支持 |
| tunnel 多路复用 | 不支持 |
| raw/ws/h2/h3 transport | 支持 |
| TLS | 支持 |
| REALITY/Vision | 支持，仅 raw transport |
| token 格式 | UUID |
| 服务端出站目标 | 仅允许公网 IP |

## 关键字段含义

| 字段 | 位置 | 含义 |
| --- | --- | --- |
| `tunnel_protocol: "vless"` | server/client | 启用 VLESS 风格封装。 |
| `token` | server/client | VLESS user id，必须是 UUID。 |
| `tunnel_transport` | server/client | 承载层。REALITY/Vision 必须使用 `raw`。 |
| `tunnel_security: "reality"` | server/client | 启用 REALITY。普通 VLESS 不设置或设置为 `none`。 |
| `tunnel_flow` | server/client | Vision 常用 `xtls-rprx-vision`。 |
| `reality_server_name` | client | client 发送的 REALITY serverName。 |
| `reality_server_names` | server | server 允许的 serverName 列表，逗号分隔或 JSON 数组。 |
| `reality_public_key` | client | REALITY public key。`tcptun config` 同时生成两端配置时会从 `reality_private_key` 派生。 |
| `reality_private_key` | server | REALITY private key。在交互式 `tcptun config` 中留空会自动生成。 |
| `reality_short_id` | client | client 使用的 shortId 十六进制字符串，可为空。 |
| `reality_short_ids` | server | server 允许的 shortId 列表，可为空列表。 |
| `reality_fingerprint` | client | uTLS fingerprint，例如 `chrome`。 |
| `reality_dest` | server | REALITY fallback 目标，格式 `host:port`。 |
| `reality_spider_x` | client | 兼容 Xray 配置字段，通常为 `/`。 |

## 普通 VLESS 配置

生成：

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

## VLESS REALITY/Vision 配置

REALITY/Vision 要求：

- `tunnel_protocol` 为 `vless`。
- `tunnel_transport` 为 `raw`。
- `tunnel_security` 为 `reality`。
- 不能同时启用 `tunnel_tls`。
- `token` 必须是 UUID。
- client 和 server 的 REALITY key、serverName、shortId、flow 必须匹配。

生成示例：

```sh
bin/tcptun config \
  --protocol vless \
  --transport raw \
  --tunnel-security reality \
  --flow xtls-rprx-vision \
  --server-addr proxy.example.com:443
```

交互模式下，如果 `reality_private_key` 留空，程序会自动生成新的 X25519 key pair。生成的 `server.json` 会写入 `reality_private_key`，生成的 `client.json` 会写入匹配的 `reality_public_key`。

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

## 和 Xray 配置对应关系

| Xray 字段 | 本项目字段 |
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

## 运行

```sh
bin/tcptun server
bin/tcptun client
```

关闭 mux 时，VLESS 会为每个 UDP 目标打开一条 VLESS UDP 请求来支持 UDP relay。开启 `tunnel_mux` 时，VLESS 会在一条共享 VLESS mux command 连接上使用 Xray 兼容 mux.cool 帧，因此可以和 Xray mux 互通，也可以和另一端 tcptun 互通。
