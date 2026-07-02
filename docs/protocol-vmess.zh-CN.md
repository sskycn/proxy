# VMess 协议配置

English version: [protocol-vmess.md](protocol-vmess.md)

`vmess` 用于兼容 Xray VMess AEAD TCP 和 UDP 请求封装。它适合需要对接 VMess AEAD 的场景，但不是本项目功能最完整的协议。

## 适用场景

- 需要与 Xray VMess AEAD TCP/UDP 配置互通。
- 对端 VMess 用户 id 是 UUID。
- 对端使用 `security: "none"`。

## 能力和限制

| 能力 | 状态 |
| --- | --- |
| TCP 代理 | 支持 |
| SOCKS5 UDP relay | 支持 |
| tunnel 多路复用 | 不支持 |
| raw/ws/h2/h3 transport | 支持 |
| TLS | 支持 |
| REALITY/Vision | 不支持 |
| token 格式 | UUID |
| VMess security | 仅 `none` |
| 服务端出站目标 | 仅允许公网 IP |

当前兼容目标：

- VMess AEAD header。
- TCP 和 UDP command。
- `security: "none"`。
- 支持 Xray 默认 chunk stream/chunk masking 相关选项。

当前不支持：

- AES-GCM 或 ChaCha20-Poly1305 body security。
- VMess mux command。
- global padding。
- authenticated length。

## 关键字段含义

| 字段 | 位置 | 含义 |
| --- | --- | --- |
| `tunnel_protocol: "vmess"` | server/client | 启用 VMess AEAD TCP/UDP 封装。 |
| `token` | server/client | VMess user id，必须是 UUID。 |
| `tunnel_transport` | server/client | 承载层，支持 `raw`、`ws`、`h2`、`h3`。 |
| `tunnel_tls` | client | 是否用 TLS 连接服务端。 |
| `tunnel_tls_cert` / `tunnel_tls_key` | server | TLS 证书和私钥。 |
| `tunnel_tls_server_name` | client | TLS SNI 和证书校验名称。 |

## 生成配置

```sh
bin/tcptun config --protocol vmess --server-addr proxy.example.com:9443
```

WebSocket + TLS：

```sh
bin/tcptun config \
  --protocol vmess \
  --transport ws \
  --server-addr proxy.example.com:443 \
  --tunnel-path /tcptun \
  --tls \
  --tls-server-name proxy.example.com
```

## server.json 示例

```json
{
  "mode": "server",
  "listen_addrs": ["0.0.0.0:9443"],
  "token": "00000000-0000-4000-8000-000000000000",
  "tunnel_protocol": "vmess",
  "tunnel_transport": "raw",
  "tunnel_path": "/proxy"
}
```

## client.json 示例

```json
{
  "mode": "client",
  "listen_addrs": ["127.0.0.1:1080"],
  "server_addr": "proxy.example.com:9443",
  "token": "00000000-0000-4000-8000-000000000000",
  "tunnel_protocol": "vmess",
  "tunnel_transport": "raw",
  "tunnel_path": "/proxy",
  "upstream_protocol": "socks5"
}
```

## 和 Xray 配置对应关系

| Xray 字段 | 本项目字段 |
| --- | --- |
| `protocol: "vmess"` | `tunnel_protocol: "vmess"` |
| `users[].id` | `token` |
| `users[].security: "none"` | 当前唯一支持的 VMess body security |
| `streamSettings.network: "tcp"` | `tunnel_transport: "raw"` |
| `streamSettings.network: "ws"` | `tunnel_transport: "ws"` |
| `streamSettings.wsSettings.path` | `tunnel_path` |
| `streamSettings.security: "tls"` | client `tunnel_tls: true`，server 配置证书和私钥 |

## TLS 示例

server:

```json
{
  "mode": "server",
  "listen_addrs": ["0.0.0.0:443"],
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
  "listen_addrs": ["127.0.0.1:1080"],
  "server_addr": "proxy.example.com:443",
  "token": "00000000-0000-4000-8000-000000000000",
  "tunnel_protocol": "vmess",
  "tunnel_transport": "raw",
  "tunnel_tls": true,
  "tunnel_tls_server_name": "proxy.example.com"
}
```

## 运行

```sh
bin/tcptun server
bin/tcptun client
```

关闭 mux 时，VMess 会为每个 UDP 目标打开一条 VMess UDP 请求来支持 UDP relay。开启 `tunnel_mux` 时，VMess 会在一条共享 VMess TCP 连接上使用 Xray 兼容 mux.cool 帧，因此可以和 Xray mux 互通，也可以和另一端 tcptun 互通。
