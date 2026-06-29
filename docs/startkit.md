# Proxy Protocol Startkit

本文档是本项目 client/server 模式的协议配置入口。每个隧道协议都有独立文档，便于按部署目标选择配置。

- [Custom 协议](protocol-custom.md)
- [VLESS 协议](protocol-vless.md)
- [VMess 协议](protocol-vmess.md)
- [Trojan 协议](protocol-trojan.md)

所有示例都使用占位值。不要把真实服务器地址、UUID、Trojan password、REALITY public key/private key 或证书私钥提交到仓库。

## 快速生成配置

不带参数运行时进入交互式配置向导：

```sh
bin/proxy config
```

直接生成指定协议配置：

```sh
bin/proxy config --protocol custom --server-addr proxy.example.com:9443
bin/proxy config --protocol vless --server-addr proxy.example.com:9443
bin/proxy config --protocol vmess --server-addr proxy.example.com:9443
bin/proxy config --protocol trojan --server-addr proxy.example.com:443 --tls --tls-server-name proxy.example.com
```

默认会生成两份文件：

- `server.json`：给 `proxy server` 使用。
- `client.json`：给 `proxy client` 使用。

默认运行规则：

- `proxy server` 默认读取可执行文件所在目录下的 `server.json`。
- `proxy client` 默认读取可执行文件所在目录下的 `client.json`。
- `proxy` 或 `proxy local` 默认读取可执行文件所在目录下的 `config.json`。
- 显式传入 `--config <path>` 时使用指定配置文件。
- 显式传入 `--config ""` 时禁用配置加载和写回。

## 运行方式

服务端：

```sh
bin/proxy server
```

客户端：

```sh
bin/proxy client
```

也可以直接用命令行覆盖配置：

```sh
bin/proxy server --config /etc/proxy/server.json
bin/proxy client --config /etc/proxy/client.json
```

## 公共配置字段

这些字段对多个协议通用。

| 字段 | 适用模式 | 含义 |
| --- | --- | --- |
| `mode` | server/client/local | 运行模式。`server` 监听隧道连接，`client` 本地开 mixed 代理并转发到隧道服务端，`local` 发现网关代理。 |
| `listen_addr` | server/client/local | 本地监听地址。server 常用 `0.0.0.0:9443`，client/local 常用 `127.0.0.1:1080`。 |
| `server_addr` | client | client 连接的隧道服务端地址，格式为 `host:port`。 |
| `token` | server/client | 认证材料。不同协议含义不同：custom 是共享 token，VLESS/VMess 是 UUID，Trojan 是 password。 |
| `tunnel_protocol` | server/client | 隧道协议：`custom`、`vless`、`vmess`、`trojan`。 |
| `tunnel_transport` | server/client | 承载层：`raw`、`ws`、`h2`、`h3`。 |
| `tunnel_path` | server/client | WebSocket、HTTP/2、HTTP/3 使用的路径，例如 `/proxy`。raw transport 通常不关心该值。 |
| `tunnel_tls` | client | client 是否用 TLS 连接服务端。raw/ws/h2 可用；h3 固定使用 HTTPS/QUIC。 |
| `tunnel_tls_cert` | server | server TLS 证书路径。 |
| `tunnel_tls_key` | server | server TLS 私钥路径。 |
| `tunnel_tls_server_name` | client | TLS SNI 和证书校验名称。 |
| `tunnel_tls_insecure` | client | 是否跳过 TLS 证书校验。只用于测试或临时环境。 |
| `tunnel_security` | server/client | 额外安全层。目前主要用于 VLESS REALITY，值为 `reality`。普通 TLS 不写在这里。 |
| `tunnel_flow` | server/client | VLESS flow，例如 `xtls-rprx-vision`。 |
| `tunnel_mux` | server/client | 是否开启本项目 tunnel 多路复用。当前只有 custom 协议支持。 |
| `upstream_protocol` | client/local | 走上游时使用的本地上游协议，支持 `socks5` 和 `mixed`。 |
| `force_upstream` | client/local | 强制走上游规则，支持域名、域名前缀、域名后缀、IP、CIDR 和范围。 |

## Transport 选择

| transport | 使用场景 | 说明 |
| --- | --- | --- |
| `raw` | 直连、TLS、Trojan、REALITY | 开销最低。需要过普通 TCP 端口时优先使用。 |
| `ws` | nginx HTTP 反向代理、常见 CDN | HTTP/1.1 WebSocket。适合放在 Web/CDN 基础设施后面。 |
| `h2` | HTTP/2 upstream | 无证书时 server 使用 h2c；配置证书后使用 TLS HTTP/2。 |
| `h3` | HTTP/3/QUIC | server 必须配置证书和私钥；client 使用 HTTPS/QUIC。 |

## 协议能力对比

| 协议 | TCP | SOCKS5 UDP relay | tunnel mux | TLS | REALITY/Vision | Xray 兼容目标 |
| --- | --- | --- | --- | --- | --- | --- |
| custom | 支持 | 支持 | 支持 | 支持 | 不支持 | 不适用 |
| vless | 支持 | 不支持 | 不支持 | 支持 | 支持 | VLESS TCP，REALITY/Vision |
| vmess | 支持 | 不支持 | 不支持 | 支持 | 不支持 | VMess AEAD TCP，security none |
| trojan | 支持 | 不支持 | 不支持 | 推荐 | 不支持 | Trojan TCP |

## 配置建议

- 自己控制 client/server 两端，并希望性能和功能优先：优先使用 `custom`。
- 需要对接 Xray VLESS REALITY/Vision：使用 `vless`、`raw`、`tunnel_security: reality`。
- 需要对接 Xray VMess AEAD TCP：使用 `vmess`，并确认对端是 `security: "none"`。
- 需要 Trojan 兼容：使用 `trojan`，通常搭配 `raw` + TLS。
- 需要放在 nginx 或 CDN 后面：优先考虑 `ws`，再按基础设施能力考虑 `h2` 或 `h3`。
