# Custom 协议配置

`custom` 是本项目自定义的轻量隧道协议，也是默认协议。它面向本项目自己的 client/server 组合，重点是低开销、功能完整和实现可控。

## 适用场景

- client 和 server 都使用本项目程序。
- 希望支持 TCP、SOCKS5 UDP relay 和 tunnel 多路复用。
- 希望减少 WebSocket、HTTP/2、HTTP/3 场景下的握手成本。
- 不需要兼容 Xray、V2Ray、Trojan-Go 等外部协议栈。

## 能力和限制

| 能力 | 状态 |
| --- | --- |
| TCP 代理 | 支持 |
| SOCKS5 UDP relay | 支持 |
| tunnel 多路复用 | 支持 |
| raw/ws/h2/h3 transport | 支持 |
| TLS | 支持 |
| REALITY/Vision | 不支持 |
| 外部 Xray 兼容 | 不适用 |

## 关键字段含义

| 字段 | 位置 | 含义 |
| --- | --- | --- |
| `tunnel_protocol: "custom"` | server/client | 启用本项目自定义协议。 |
| `token` | server/client | 共享认证 token。server 配置为空时不强制认证；生产环境建议总是设置。 |
| `tunnel_transport` | server/client | 承载层。默认 `raw`，也可使用 `ws`、`h2`、`h3`。 |
| `tunnel_mux` | server/client | 是否开启多路复用。custom 默认建议开启。 |
| `tunnel_path` | server/client | HTTP/WebSocket 类 transport 使用的路径。raw transport 可保留默认值。 |
| `tunnel_tls` | client | client 是否使用 TLS 连接 server。 |
| `tunnel_tls_cert` / `tunnel_tls_key` | server | server 侧 TLS 证书和私钥。 |

## 生成配置

交互式生成：

```sh
bin/proxy config
```

非交互生成：

```sh
bin/proxy config --protocol custom --server-addr proxy.example.com:9443
```

使用 WebSocket transport：

```sh
bin/proxy config \
  --protocol custom \
  --transport ws \
  --server-addr proxy.example.com:443 \
  --tunnel-path /proxy \
  --tls \
  --tls-server-name proxy.example.com
```

## server.json 示例

```json
{
  "mode": "server",
  "listen_addr": "0.0.0.0:9443",
  "token": "CHANGE_ME_RANDOM_TOKEN",
  "tunnel_protocol": "custom",
  "tunnel_transport": "raw",
  "tunnel_path": "/proxy",
  "tunnel_mux": true,
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

## client.json 示例

```json
{
  "mode": "client",
  "listen_addr": "127.0.0.1:1080",
  "server_addr": "proxy.example.com:9443",
  "token": "CHANGE_ME_RANDOM_TOKEN",
  "tunnel_protocol": "custom",
  "tunnel_transport": "raw",
  "tunnel_path": "/proxy",
  "tunnel_mux": true,
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

## TLS 配置

raw transport 也可以跑在 TLS 内：

server:

```json
{
  "mode": "server",
  "listen_addr": "0.0.0.0:443",
  "token": "CHANGE_ME_RANDOM_TOKEN",
  "tunnel_protocol": "custom",
  "tunnel_transport": "raw",
  "tunnel_tls_cert": "/etc/proxy/server.crt",
  "tunnel_tls_key": "/etc/proxy/server.key"
}
```

client:

```json
{
  "mode": "client",
  "listen_addr": "127.0.0.1:1080",
  "server_addr": "proxy.example.com:443",
  "token": "CHANGE_ME_RANDOM_TOKEN",
  "tunnel_protocol": "custom",
  "tunnel_transport": "raw",
  "tunnel_tls": true,
  "tunnel_tls_server_name": "proxy.example.com"
}
```

## 多路复用

`custom` 支持 tunnel mux。开启后，client 会维持一条到底层 transport 的共享连接，并为每条 TCP 连接或 UDP relay 开一个逻辑 stream。

建议：

- `raw` 内网或稳定专线：开启 mux 可以减少连接数。
- `ws`、`h2`、`h3` 经过代理/CDN：开启 mux 可以减少握手与 HTTP 层连接建立成本。
- 如果排查问题或遇到中间网络对长连接不友好，可以临时设置 `"tunnel_mux": false`。

## 运行

```sh
bin/proxy server
bin/proxy client
```

客户端启动后，本地代理地址默认是：

```text
127.0.0.1:1080
```
