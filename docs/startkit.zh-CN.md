# tcptun Protocol Startkit

English version: [startkit.md](startkit.md)

本文档是本项目 client/server 模式的协议配置入口。每个隧道协议都有独立文档，便于按部署目标选择配置。

- [Native 协议](protocol-native.zh-CN.md)
- [VLESS 协议](protocol-vless.zh-CN.md)
- [VMess 协议](protocol-vmess.zh-CN.md)
- [Trojan 协议](protocol-trojan.zh-CN.md)

所有示例都使用占位值。不要把真实服务器地址、UUID、Trojan password、REALITY public key/private key 或证书私钥提交到仓库。

## 快速生成配置

不带参数运行时进入交互式配置向导：

```sh
bin/tcptun config
```

直接生成指定协议配置：

```sh
bin/tcptun config --protocol native --server-addr proxy.example.com:9443
bin/tcptun config --protocol vless --server-addr proxy.example.com:9443
bin/tcptun config --protocol vmess --server-addr proxy.example.com:9443
bin/tcptun config --protocol trojan --server-addr proxy.example.com:443 --tls --tls-server-name proxy.example.com
```

默认会生成三份文件：

- `server.json`：给 `tcptun server` 使用。
- `client.json`：给 `tcptun client` 使用。
- `route.json`：给 local/client 路由规则和学习到的直连失败目标使用。

默认运行规则：

- `tcptun server` 默认读取 `server.json`。
- `tcptun client` 默认读取 `client.json`。
- `tcptun` 或 `tcptun local` 默认读取 `config.json`。
- 相对配置路径按顺序搜索：程序所在目录、当前工作目录、`~/.config/tcptun`。
- 如果三个位置都不存在，写回时使用程序所在目录。
- 显式传入 `--config <path>` 时使用指定配置文件。
- 显式传入 `--config ""` 时禁用运行配置加载。
- 显式传入 `--route-config <path>` 时使用指定路由配置。
- 显式传入 `--route-config ""` 时禁用路由加载和写回。

## 运行方式

服务端：

```sh
bin/tcptun server
```

客户端：

```sh
bin/tcptun client
```

也可以直接用命令行覆盖配置：

```sh
bin/tcptun server --config /etc/tcptun/server.json
bin/tcptun client --config /etc/tcptun/client.json
```

## 运行配置字段

这些字段会从 `config.json`、`server.json` 或 `client.json` 加载。

| 字段 | 适用模式 | 含义 |
| --- | --- | --- |
| `mode` | server/client/local | 运行模式。`server` 监听隧道连接，`client` 本地开 mixed 代理并转发到隧道服务端，`local` 发现网关代理。 |
| `listen_addr` | server/client/local | 本地监听地址。server 常用 `0.0.0.0:9443`，client/local 常用 `127.0.0.1:1080`。 |
| `server_addr` | client | client 连接的隧道服务端地址，格式为 `host:port`。 |
| `token` | server/client | 认证材料。不同协议含义不同：native 是共享 token，VLESS/VMess 是 UUID，Trojan 是 password。 |
| `tunnel_protocol` | server/client | 隧道协议：`native`、`vless`、`vmess`、`trojan`。 |
| `tunnel_transport` | server/client | 承载层：`raw`、`ws`、`h2`、`h3`。 |
| `tunnel_path` | server/client | WebSocket、HTTP/2、HTTP/3 使用的路径，例如 `/proxy`。raw transport 通常不关心该值。 |
| `tunnel_tls` | client | client 是否用 TLS 连接服务端。raw/ws/h2 可用；h3 固定使用 HTTPS/QUIC。 |
| `tunnel_tls_cert` | server | server TLS 证书路径。 |
| `tunnel_tls_key` | server | server TLS 私钥路径。 |
| `tunnel_tls_server_name` | client | TLS SNI 和证书校验名称。 |
| `tunnel_tls_insecure` | client | 是否跳过 TLS 证书校验。只用于测试或临时环境。 |
| `tunnel_security` | server/client | 额外安全层。目前主要用于 VLESS REALITY，值为 `reality`。普通 TLS 不写在这里。 |
| `tunnel_flow` | server/client | VLESS flow，例如 `xtls-rprx-vision`。 |
| `tunnel_mux` | server/client | 是否开启本项目 tunnel 多路复用。当前只有 native 协议支持。 |
| `upstream_protocol` | client/local | 走上游时使用的本地上游协议，支持 `socks5` 和 `mixed`。 |
| `socks5_username` | client/local | 本地 SOCKS5 用户名。用户名或密码任一非空时，会对 SOCKS5 客户端启用 username/password 认证。 |
| `socks5_password` | client/local | 本地 SOCKS5 密码。 |
| `upstream_socks5_username` | local | 连接上游 SOCKS5 网关时使用的用户名。 |
| `upstream_socks5_password` | local | 连接上游 SOCKS5 网关时使用的密码。 |
| `direct_probe_timeout` | client/local | 直连优先探测的超时时间，超时后走上游。默认 `500ms`；JSON 中支持 `"500ms"` 这类 Go duration 字符串。 |
| `scan_retry_interval` | local | 自动扫描本机 IPv4 网段没有找到可达网关代理时，下一次重扫前的等待时间。默认 `5s`。 |

server 模式下，TCP 和 UDP 出站目标只允许公网 IP。域名目标会在服务端解析，私有、回环、链路本地、组播、CGNAT 和保留网段会在拨号前被拒绝；native UDP 指向非公网目标的数据包会被直接丢弃。

## CLI/API 运行参数

这些设置可以通过命令行 flag 或 Go `Config` 字段设置，但不会从运行配置 JSON 文件加载。

| 设置 | 适用模式 | 含义 |
| --- | --- | --- |
| `gateway_ip` / `--gateway-ip` | local | 上游网关 IP。留空时仅在本机存在内网 IPv4 地址时自动发现。 |
| `gateway_port` / `--gateway-port` | local | 网关代理端口，默认 `1080`。 |
| `dial_timeout` / `--dial-timeout` | server/client/local | TCP 拨号超时时间，用于上游、隧道和网关检测，默认 `5s`。 |
| `refresh_interval` / `--refresh-interval` | local | 检查本机 IPv4 变化的间隔。只有本机 IPv4 变化后才重新发现网关。`0` 表示禁用刷新。 |
| `scan_timeout` / `--scan-timeout` | local | 扫描本机 IPv4 网段时每个 IP 的探测超时。 |
| `scan_workers` / `--scan-workers` | local | 扫描本机 IPv4 网段时使用的并发 worker 数。 |
| `buffer_size` / `--buffer-size` | server/client/local | 每个方向的复制缓冲区大小；低于 4096 时会提升到 4096。 |
| `verbose` / `--verbose` | server/client/local | 是否输出调试日志。访问日志始终会输出。 |

## 路由配置字段

路由字段写在 `route.json`，不写入 `server.json`、`client.json` 或 `config.json`。`tcptun config` 默认会写出一个空的路由文件。

local 模式自动发现网关时，如果本机内网 IPv4 网段扫描没有找到可达代理，会等待 `scan_retry_interval` 后重新扫描同一批本机内网 IPv4 网段。至少找到一个代理、进程退出或 discovery context 被取消时，重试循环结束。如果扫描找到多个可达代理，会全部保留为上游候选，并按测得的连接延迟排序。新的源 IP 优先选择当前已知最快的候选；已有源 IP 会继续使用已绑定上游，直到该上游连接失败或上游协议握手失败。网关发现和网段扫描仍然只会在本机存在内网 IPv4 地址时触发；启动后也仍然只有本机 IPv4 地址集合发生变化才会重新触发。

| 字段 | 适用模式 | 含义 |
| --- | --- | --- |
| `force_upstream.domains` | client/local | 精确域名匹配。学习到的域名直连失败目标会写到这里，除非已有规则覆盖。 |
| `force_upstream.domain_regexes` | client/local | Go/RE2 正则表达式，匹配规范化后的小写 host。 |
| `force_upstream.domain_suffixes` | client/local | 匹配该后缀本身及所有子域名。写回时会规范化、去重和排序。 |
| `force_upstream.ips` | client/local | 精确 IP 匹配。学习到的 IP 直连失败目标会写到这里，除非已有规则覆盖。 |
| `force_upstream.ip_cidrs` | client/local | CIDR 前缀匹配。 |
| `force_upstream.ip_ranges` | client/local | CIDR 风格范围的别名，解析方式与 `ip_cidrs` 相同。 |

程序退出前会把学习到的 TCP 直连失败目标合并到 `route.json` 或 `--route-config` 指定文件。当同一可注册主域名下超过 3 个子域名出现在 `force_upstream.domains` 中时，程序会把该主域名提升到 `force_upstream.domain_suffixes`，并删除已被覆盖的精确域名记录。

## 直连优先与快速失败

对于可解析目标的 TCP 代理流量，强制走上游规则优先级最高。否则程序会先尝试直连，再按需走上游。

- 普通 HTTP 且通常不带请求体的请求，需要在 `direct_probe_timeout` 内收到直连目标返回的首字节。
- HTTP CONNECT 和 SOCKS5 CONNECT 会等待客户端首个隧道数据包，把它发给直连目标，并要求在 `direct_probe_timeout` 内收到直连目标返回的首字节。
- 探测失败时，该目标会被标记为仅走上游，首个数据包会重放到上游路径，后续连接跳过直连尝试。
- UDP 使用保守规则：内网 UDP 目标直连，其他 UDP 目标走上游。在 client/server 模式下，流量到达隧道服务端后仍会执行仅公网 IP 出站规则。

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
| native | 支持 | 支持 | 支持 | 支持 | 不支持 | 不适用 |
| vless | 支持 | 不支持 | 不支持 | 支持 | 支持 | VLESS TCP，REALITY/Vision |
| vmess | 支持 | 不支持 | 不支持 | 支持 | 不支持 | VMess AEAD TCP，security none |
| trojan | 支持 | 不支持 | 不支持 | 推荐 | 不支持 | Trojan TCP |

## 配置建议

- 自己控制 client/server 两端，并希望性能和功能优先：优先使用 `native`。
- 需要对接 Xray VLESS REALITY/Vision：使用 `vless`、`raw`、`tunnel_security: reality`。
- 需要对接 Xray VMess AEAD TCP：使用 `vmess`，并确认对端是 `security: "none"`。
- 需要 Trojan 兼容：使用 `trojan`，通常搭配 `raw` + TLS。
- 需要放在 nginx 或 CDN 后面：优先考虑 `ws`，再按基础设施能力考虑 `h2` 或 `h3`。
