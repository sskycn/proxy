# Trojan 协议配置

English version: [protocol-trojan.md](protocol-trojan.md)

`trojan` 用于标准 Trojan TCP 请求封装。Trojan 常见部署方式是 raw transport 搭配 TLS，因此生产配置通常需要服务端证书和客户端 SNI。

## 适用场景

- 需要兼容 Trojan TCP。
- 希望用 password 作为认证材料。
- 部署入口是 TLS 端口，例如 `443`。

## 能力和限制

| 能力 | 状态 |
| --- | --- |
| TCP 代理 | 支持 |
| SOCKS5 UDP relay | 不支持 |
| tunnel 多路复用 | 不支持 |
| raw/ws/h2/h3 transport | 支持 |
| TLS | 强烈建议 |
| REALITY/Vision | 不支持 |
| token 格式 | Trojan password |
| 服务端出站目标 | 仅允许公网 IP |

## 关键字段含义

| 字段 | 位置 | 含义 |
| --- | --- | --- |
| `tunnel_protocol: "trojan"` | server/client | 启用 Trojan TCP 封装。 |
| `token` | server/client | Trojan password。协议内会使用 password hash。 |
| `tunnel_transport` | server/client | 推荐 `raw`。也可由本项目 transport 层承载在 `ws`、`h2`、`h3` 上。 |
| `tunnel_tls` | client | 是否使用 TLS 连接服务端。Trojan 常见部署应启用。 |
| `tunnel_tls_cert` / `tunnel_tls_key` | server | server 侧 TLS 证书和私钥。 |
| `tunnel_tls_server_name` | client | TLS SNI 和证书校验名称。 |
| `tunnel_tls_insecure` | client | 跳过 TLS 证书校验。仅用于测试。 |

## 生成配置

```sh
bin/tcptun config \
  --protocol trojan \
  --transport raw \
  --server-addr proxy.example.com:443 \
  --tls \
  --tls-server-name proxy.example.com
```

如果已有证书路径，可以在生成时写入：

```sh
bin/tcptun config \
  --protocol trojan \
  --transport raw \
  --server-addr proxy.example.com:443 \
  --tls \
  --tls-cert /etc/tcptun/server.crt \
  --tls-key /etc/tcptun/server.key \
  --tls-server-name proxy.example.com
```

## server.json 示例

```json
{
  "mode": "server",
  "listen_addr": "0.0.0.0:443",
  "token": "CHANGE_ME_TROJAN_PASSWORD",
  "tunnel_protocol": "trojan",
  "tunnel_transport": "raw",
  "tunnel_path": "/proxy",
  "tunnel_tls_cert": "/etc/tcptun/server.crt",
  "tunnel_tls_key": "/etc/tcptun/server.key"
}
```

## client.json 示例

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
  "upstream_protocol": "socks5"
}
```

## 和 Xray Trojan 配置对应关系

| Xray 字段 | 本项目字段 |
| --- | --- |
| `protocol: "trojan"` | `tunnel_protocol: "trojan"` |
| `servers[].password` 或 `clients[].password` | `token` |
| `streamSettings.network: "tcp"` | `tunnel_transport: "raw"` |
| `streamSettings.security: "tls"` | client `tunnel_tls: true`，server 配置证书和私钥 |
| `tlsSettings.serverName` | `tunnel_tls_server_name` |

## TLS 注意事项

- 生产环境不要使用 `tunnel_tls_insecure: true`。
- `tunnel_tls_server_name` 应与证书中的域名匹配。
- `server_addr` 可以是域名或 IP；如果是 IP，但证书是域名签发，client 仍应设置 `tunnel_tls_server_name` 为证书域名。
- 证书私钥路径只应放在服务端配置中，不要写入 client 配置。

## 运行

```sh
bin/tcptun server
bin/tcptun client
```

Trojan 当前只承载 TCP。需要 UDP relay 或 tunnel mux 时请选择 `native` 协议。
