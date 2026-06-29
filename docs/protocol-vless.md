# VLESS 协议配置

`vless` 用于 VLESS 风格的 TCP 请求封装。本项目支持普通 VLESS TCP，也支持 Xray REALITY/Vision 兼容模式。

## 适用场景

- 需要和 Xray VLESS TCP 配置兼容。
- 需要使用 REALITY/Vision。
- 希望 token 使用 UUID 形式，便于和 Xray 用户 id 对齐。

## 能力和限制

| 能力 | 状态 |
| --- | --- |
| TCP 代理 | 支持 |
| SOCKS5 UDP relay | 不支持 |
| tunnel 多路复用 | 不支持 |
| raw/ws/h2/h3 transport | 支持 |
| TLS | 支持 |
| REALITY/Vision | 支持，仅 raw transport |
| token 格式 | UUID |

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
| `reality_public_key` | client | REALITY public key。 |
| `reality_private_key` | server | REALITY private key。 |
| `reality_short_id` | client | client 使用的 shortId 十六进制字符串，可为空。 |
| `reality_short_ids` | server | server 允许的 shortId 列表，可为空列表。 |
| `reality_fingerprint` | client | uTLS fingerprint，例如 `chrome`。 |
| `reality_dest` | server | REALITY fallback 目标，格式 `host:port`。 |
| `reality_spider_x` | client | 兼容 Xray 配置字段，通常为 `/`。 |

## 普通 VLESS 配置

生成：

```sh
bin/proxy config --protocol vless --server-addr proxy.example.com:9443
```

server:

```json
{
  "mode": "server",
  "listen_addr": "0.0.0.0:9443",
  "token": "00000000-0000-4000-8000-000000000000",
  "tunnel_protocol": "vless",
  "tunnel_transport": "raw",
  "tunnel_path": "/proxy",
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

client:

```json
{
  "mode": "client",
  "listen_addr": "127.0.0.1:1080",
  "server_addr": "proxy.example.com:9443",
  "token": "00000000-0000-4000-8000-000000000000",
  "tunnel_protocol": "vless",
  "tunnel_transport": "raw",
  "tunnel_path": "/proxy",
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
bin/proxy config \
  --protocol vless \
  --transport raw \
  --tunnel-security reality \
  --flow xtls-rprx-vision \
  --server-addr proxy.example.com:443
```

server:

```json
{
  "mode": "server",
  "listen_addr": "0.0.0.0:443",
  "token": "00000000-0000-4000-8000-000000000000",
  "tunnel_protocol": "vless",
  "tunnel_transport": "raw",
  "tunnel_security": "reality",
  "tunnel_flow": "xtls-rprx-vision",
  "reality_private_key": "REALITY_PRIVATE_KEY",
  "reality_server_names": ["example.com"],
  "reality_short_ids": [""],
  "reality_dest": "example.com:443",
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

client:

```json
{
  "mode": "client",
  "listen_addr": "127.0.0.1:1080",
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
bin/proxy server
bin/proxy client
```

本项目的 VLESS 当前只承载 TCP。需要 UDP relay 或 tunnel mux 时请选择 `custom` 协议。
