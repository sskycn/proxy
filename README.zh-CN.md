# tcptun

本地 mixed 代理转发程序，使用 Go 编写。

这个工具会在本机打开一个 mixed 代理端口，并把需要走上游的流量转发到网关。上游协议可配置，默认使用 SOCKS5。它偏性能优先设计：连接转发使用复用缓冲区，HTTP 和 SOCKS5 请求只解析到足够完成直连/上游路由选择。

[English](README.md) | 简体中文

## 功能

- 默认监听本机 `127.0.0.1:1080`。
- 默认转发到网关代理端口 `1080`。
- 本地支持 mixed 代理流量，包括 SOCKS5、HTTP 代理、HTTP CONNECT。
- 本地 SOCKS5 入口支持 username/password 认证。
- 默认使用 SOCKS5 作为上游协议，也支持 `mixed` 上游模式。
- 连接上游 SOCKS5 网关时支持 username/password 认证。
- 支持 `tcptun`、`tcptun local`、`tcptun client`、`tcptun server` 四种命令形态，并支持 `native`、`vless`、`vmess`、`trojan` 隧道协议。
- client/server 隧道可承载在 raw TCP、WebSocket、HTTP/2 或 HTTP/3 transport 上。
- client/server 隧道默认启用多路复用，多条 TCP 连接和 UDP relay 可以共享同一条上游 tunnel transport 连接。
- 支持 SOCKS5 UDP ASSOCIATE，可转发 UDP 流量。
- 在终端输出紧凑访问日志；直连日志会省略代理字段。
- 自动发现默认网关 IP。
- 仅在本机存在内网 IPv4 地址时，检测发现到的网关端口是否可连通。
- 如果默认网关不可连通，自动扫描本机所在的内网 IPv4 网段，并保留所有可连通的上游候选。
- 优先使用响应更快的扫描上游，同时保持同一个源 IP 绑定到同一个上游，直到该上游失效。
- 运行期间定时刷新可用上游，网络变化后新连接会使用新的目标地址集合。
- 对私有地址、回环地址、链路本地地址、`localhost` 和 `.local` 目标直连，不转发到上游。
- TCP 目标优先直连；如果直连失败，会记住该目标，后续连接直接走上游。
- 支持通过 `route.json` 配置强制走上游规则，可按精确域名、域名正则、域名后缀、精确 IP 和 IP CIDR/网段匹配。
- 程序退出前会把学习到的直连失败目标写回 `route.json`；文件不存在会自动创建，已有规则会去重。
- 命令行基于 `pkg.gostartkit.com/cmd v0.2.1`。

## 环境要求

- Go 1.25 或更新版本。

## 构建

```sh
make build
```

构建产物路径：

```text
bin/tcptun
```

也可以直接使用 Go 构建：

```sh
go build -trimpath -ldflags "-s -w" -o bin/tcptun ./cmd/tcptun
```

## 运行

使用自动网关发现启动：

```sh
make run
```

或运行已构建的二进制：

```sh
bin/tcptun
```

修改本机监听端口：

```sh
bin/tcptun --listen 127.0.0.1:1081
```

指定已知网关 IP：

```sh
bin/tcptun --gateway-ip 192.168.1.1
```

指定网关代理端口：

```sh
bin/tcptun --gateway-port 7890
```

使用 mixed 上游网关，而不是默认 SOCKS5 上游：

```sh
bin/tcptun --upstream-protocol mixed
```

显式以 local 模式运行，并忽略 `config.json` 里的 `mode` 值：

```sh
bin/tcptun local
```

指定运行配置文件：

```sh
bin/tcptun --config ./config.json
```

指定路由配置文件：

```sh
bin/tcptun --route-config ./route.json
```

以隧道服务端运行：

```sh
bin/tcptun server --listen 0.0.0.0:9443 --token change-me
```

以隧道客户端运行：

```sh
bin/tcptun client --listen 127.0.0.1:1080 --server-addr 203.0.113.10:9443 --token change-me
```

通过 HTTP 反向代理或 CDN 使用 WebSocket：

```sh
bin/tcptun server --listen 127.0.0.1:9443 --transport ws --tunnel-path /tcptun --token change-me
bin/tcptun client --listen 127.0.0.1:1080 --server-addr proxy.example.com:443 --transport ws --tunnel-path /tcptun --tls --token change-me
```

使用 VLESS 协议运行 client/server：

```sh
bin/tcptun server --listen 0.0.0.0:9443 --tunnel-protocol vless --token 00000000-0000-4000-8000-000000000000
bin/tcptun client --server-addr 203.0.113.10:9443 --tunnel-protocol vless --token 00000000-0000-4000-8000-000000000000
```

连接 Xray VLESS REALITY/Vision 服务端。下面的值都是占位示例；不要把真实服务器地址、UUID、public key 或 private key 写进文档或提交到仓库：

```sh
bin/tcptun client \
  --listen 127.0.0.1:1080 \
  --server-addr '[2001:db8::10]:443' \
  --tunnel-protocol vless \
  --transport raw \
  --tunnel-security reality \
  --flow xtls-rprx-vision \
  --token 00000000-0000-4000-8000-000000000000 \
  --reality-server-name example.com \
  --reality-fingerprint chrome \
  --reality-public-key REALITY_PUBLIC_KEY \
  --reality-short-id ''
```

启动兼容 Xray 的 VLESS REALITY/Vision 服务端：

```sh
bin/tcptun server \
  --listen 0.0.0.0:443 \
  --tunnel-protocol vless \
  --transport raw \
  --tunnel-security reality \
  --flow xtls-rprx-vision \
  --token 00000000-0000-4000-8000-000000000000 \
  --reality-private-key REALITY_PRIVATE_KEY \
  --reality-server-names example.com \
  --reality-short-ids '' \
  --reality-dest example.com:443
```

REALITY 密钥可以用 `tcptun config`、`xray x25519` 或其他兼容工具生成。在 `tcptun config` 交互模式中，`reality_private_key` 留空会自动生成，并派生出匹配的客户端 `reality_public_key`。private key 请放在安全的部署配置中，不要进入版本控制。`--reality-spider-x` 目前用于兼容 Xray 配置字段；成功的 REALITY 握手不依赖它。

使用 Trojan 协议运行 client/server：

```sh
bin/tcptun server --listen 0.0.0.0:9443 --tunnel-protocol trojan --token change-me
bin/tcptun client --server-addr 203.0.113.10:9443 --tunnel-protocol trojan --token change-me
```

使用兼容 Xray 的 VMess over raw TCP：

```sh
bin/tcptun server --listen 0.0.0.0:9443 --tunnel-protocol vmess --transport raw --token 00000000-0000-4000-8000-000000000000
bin/tcptun client --server-addr 203.0.113.10:9443 --tunnel-protocol vmess --transport raw --token 00000000-0000-4000-8000-000000000000
```

使用兼容 Xray 常见配置的 Trojan over raw TLS：

```sh
bin/tcptun server --listen 0.0.0.0:443 --tunnel-protocol trojan --transport raw --tls-cert server.crt --tls-key server.key --token change-me
bin/tcptun client --server-addr proxy.example.com:443 --tunnel-protocol trojan --transport raw --tls --tls-server-name proxy.example.com --token change-me
```

使用 HTTP/2：

```sh
bin/tcptun server --listen 127.0.0.1:9443 --transport h2 --tunnel-path /tcptun --token change-me
bin/tcptun client --server-addr 127.0.0.1:9443 --transport h2 --tunnel-path /tcptun --token change-me
```

使用带 TLS 证书的 HTTP/3：

```sh
bin/tcptun server --listen 0.0.0.0:9443 --transport h3 --tunnel-path /tcptun --tls-cert server.crt --tls-key server.key --token change-me
bin/tcptun client --server-addr proxy.example.com:9443 --transport h3 --tunnel-path /tcptun --token change-me
```

## 网关发现逻辑

未设置 `--gateway-ip` 时，启动流程如下：

1. 检查本机是否存在内网 IPv4 地址。
2. 如果存在，自动发现系统默认网关 IP。
3. 尝试连接 `<网关IP>:<gateway-port>`。
4. 如果连接失败，扫描本机所在的内网 IPv4 网段，寻找打开了 `<gateway-port>` 的主机。
5. 如果没有找到，等待 `--scan-retry-interval` 后重新扫描。
6. 保留扫描到的所有可连通主机作为上游候选，并按连接延迟排序。

如果本机没有内网 IPv4 地址，会跳过自动网关探测和本地 IPv4 网段扫描。此时请显式设置 `--gateway-ip`。

手动设置 `--gateway-ip` 时，不会扫描网段，会直接使用该 IP。

程序运行期间会按 `--refresh-interval` 定时检查本机 IPv4 地址集合。只有本地 IPv4 地址发生变化时，才会重新发现网关或扫描本地网段。已有连接继续使用当前上游，新连接会使用变化后刷新出的目标地址集合。

当扫描得到多个上游候选时，新源地址会优先选择当前已知响应最快的上游。同一个源 IP 会持续使用同一个上游；如果该上游连接失败或上游协议握手失败，会清除绑定并尝试下一个最佳候选。

## 内网地址直连

对于 SOCKS5、SOCKS5 UDP ASSOCIATE、HTTP CONNECT 和 HTTP 代理请求，程序会解析请求目标。`route.json` 中的强制走上游规则优先级最高。否则，TCP 目标会优先直连；如果直连失败，会记住该目标为仅走上游，后续连接跳过直连尝试。普通 HTTP 且通常不带请求体的请求，要求直连目标在 `direct_probe_timeout` 内返回首字节。HTTP CONNECT 和 SOCKS5 CONNECT 会在客户端发送首个隧道数据包后探测直连响应；如果目标 TCP 能连上但一直不返回内容，程序会切到上游并重放该首包。UDP 目标保持保守规则：内网目标直连，其他目标走上游。

## 上游协议

上游协议可以通过 `--upstream-protocol` 设置，也可以写在 `config.json` 顶层的 `upstream_protocol` 字段里。支持的值是 `socks5` 和 `mixed`，默认是 `socks5`。

在 `socks5` 模式下，已解析出目标的 SOCKS5 和 HTTP 代理流量会转换成 SOCKS5 后再发往上游。未知 mixed 流量因为无法解析目标，会被拒绝。

在 `mixed` 模式下，HTTP 代理流量和未知 mixed 流量会原样发往网关。SOCKS5 TCP 和 UDP 仍使用 SOCKS5 协商，因此上游 mixed 端口需要支持 SOCKS5。

## Client/Server 子命令

更详细的协议 startkit 文档在 [docs/startkit.zh-CN.md](docs/startkit.zh-CN.md)，并已按 `native`、`vless`、`vmess`、`trojan` 拆成独立页面。

不带子命令运行 `tcptun` 时默认是 local 模式。如果 `config.json` 顶层写了 `"mode": "client"`、`"mode": "server"` 或 `"mode": "local"`，则会按配置里的模式启动。显式执行 `tcptun local`、`tcptun client` 或 `tcptun server` 时，子命令优先于配置文件里的 mode。

`tcptun local` 会强制 local 模式：本地 mixed 代理通过发现到的网关转发，即使 `config.json` 里写了 `"mode": "client"` 或 `"mode": "server"`。

`tcptun server` 会监听配置的隧道协议，并在服务端侧连接真实目标。服务端出站目标必须解析为公网 IP；私有、回环、链路本地、组播、CGNAT 和保留网段会在拨号前被拒绝。所有隧道协议都支持 TCP 和 SOCKS5 UDP relay。使用 `--listen` 指定服务端监听地址，使用 `--token` 开启认证。

`tcptun client` 保持本地 mixed 代理入口，但会把已解析出目标的上游流量封装到隧道服务端。使用 `--server-addr` 指定服务端地址，`--token` 需要和服务端一致。

默认情况下，`tcptun server` 会读取可执行文件旁边的 `server.json`，`tcptun client` 会读取可执行文件旁边的 `client.json`。显式传入 `--config <path>` 仍会覆盖这些模式默认值；传入 `--config ""` 可以禁用运行配置加载。

`tcptun config` 用于生成可直接编辑的 JSON 配置文件，不会启动代理。不带任何 flag 运行时会进入基于命令运行时的交互向导；直接回车使用默认值，也可以输入要覆盖的字段。传入 flag 时仍保持非交互生成，适合脚本使用。默认同时写出 `server.json`、`client.json` 和 `route.json`，服务端/客户端配置共享同一个自动生成的 token，并支持通过 `--protocol native|vless|vmess|trojan` 指定协议。

```sh
bin/tcptun config
bin/tcptun config --protocol native --server-addr proxy.example.com:9443
bin/tcptun config --protocol vmess --server-addr proxy.example.com:9443
bin/tcptun config --protocol trojan --transport raw --tls --tls-cert server.crt --tls-key server.key --tls-server-name proxy.example.com
bin/tcptun config --target client --output client.json --protocol vless --server-addr proxy.example.com:9443
```

配置生成参数：

```text
--target <both|server|client>     要生成的配置类型 [默认: both]
--protocol <native|vless|vmess|trojan>
--transport <raw|ws|h2|h3>
--token <string>                  为空时自动生成；VLESS/VMess 使用 UUID
--out-dir <path>                  输出目录 [默认: .]
--server-output <path>            server 配置输出路径 [默认: server.json]
--client-output <path>            client 配置输出路径 [默认: client.json]
--route-output <path>             route 配置输出路径 [默认: route.json]
-o, --output <path>               配合 --target server/client 使用的单文件输出路径
--server-listen <addr>            server 监听地址 [默认: 0.0.0.0:9443]
--client-listen <addr>            client 监听地址 [默认: 127.0.0.1:1080]
--server-addr <addr>              写入 client 配置的服务端地址
--tunnel-path <path>              HTTP/WebSocket 隧道路由路径 [默认: /proxy]
--tls, --tls-cert, --tls-key, --tls-server-name, --tls-insecure
--tunnel-security <none|reality>, --flow <string>
--reality-server-name, --reality-server-names, --reality-fingerprint
--reality-public-key, --reality-private-key, --reality-short-id, --reality-short-ids
--reality-dest, --reality-spider-x
--mux <true|false>
--client-upstream-protocol <socks5|mixed>
--client-socks5-username, --client-socks5-password
--client-upstream-socks5-username, --client-upstream-socks5-password
--force-ip-cidrs <cidr,cidr>      route 配置里的初始强制上游 CIDR
--overwrite                       覆盖已有生成文件
```

子命令别名：

- `tcptun local`：`tcptun l`、`tcptun loc`
- `tcptun client`：`tcptun c`、`tcptun cli`
- `tcptun server`：`tcptun s`、`tcptun srv`
- `tcptun config`：`tcptun cfg`、`tcptun gen`
- `tcptun version`：`tcptun v`、`tcptun ver`

隧道承载层可通过 `--transport` 或 `config.json` 里的 `tunnel_transport` 选择：

- `raw`：直接 TCP 连接到服务端。默认值，开销最低。
- `ws`：HTTP/1.1 WebSocket。最适合放在 nginx HTTP 反向代理或常见 CDN 后面。
- `h2`：HTTP/2 双向 request/response 流。服务端不配置证书时使用 h2c；配置 `--tls-cert` 和 `--tls-key` 后提供 TLS HTTP/2。
- `h3`：基于 QUIC 的 HTTP/3。服务端必须配置 `--tls-cert` 和 `--tls-key`，客户端固定使用 `https`。

raw transport 也可以运行在 TLS 内：客户端使用 `--tls`，服务端配置 `--tls-cert` 和 `--tls-key`。这是兼容 Trojan 的推荐 transport/security 组合。

隧道协议可通过 `--tunnel-protocol` 或 `config.json` 里的 `tunnel_protocol` 选择：

- `native`：本项目的轻量协议。默认值，支持 TCP、SOCKS5 UDP relay 和隧道多路复用。
- `vless`：VLESS 风格 TCP 和 UDP 请求封装。`--token` 必须是 UUID。
- `trojan`：标准 Trojan TCP 和 UDP 请求封装。`--token` 作为 Trojan password 使用。兼容 Xray 常见 Trojan 部署时，请使用 raw transport，客户端加 `--tls`，服务端配置 `--tls-cert` 和 `--tls-key`。
- `vmess`：兼容 Xray 的 VMess AEAD TCP 和 UDP 请求封装。`--token` 必须是 UUID，并作为 VMess user id 使用。当前兼容目标是 `security: "none"`、AEAD header，以及 Xray 默认的 chunk stream/chunk masking 选项；暂不支持 AES-GCM、ChaCha20-Poly1305、VMess mux command、global padding 和 authenticated length。

作为客户端兼容 Xray REALITY/Vision 时，请使用 `tcptun client`，并设置 `--transport raw`、`--tunnel-protocol vless`、`--tunnel-security reality` 和 `--flow xtls-rprx-vision`。REALITY 需要 `--reality-server-name`、`--reality-public-key` 和 UUID 格式的 `--token`；`--reality-fingerprint` 默认是 `chrome`。

作为服务端兼容 Xray REALITY/Vision 时，请使用 `tcptun server`，并设置 `--transport raw`、`--tunnel-protocol vless`、`--tunnel-security reality` 和 `--flow xtls-rprx-vision`。REALITY 服务端模式需要 `--reality-private-key`、`--reality-server-names`，以及类似 `example.com:443` 的 fallback `--reality-dest`。`tcptun config` 可以自动生成 `reality_private_key`，并派生匹配的客户端 `reality_public_key`。`--reality-short-ids` 可用于限制允许的 shortId；如果省略，则允许空 shortId。Xray 客户端需要使用匹配的 public key、server name、shortId、UUID 和 flow。

所有隧道协议都支持 client/server 模式下的 SOCKS5 UDP relay。只有 `native` 支持隧道多路复用；`vless`、`vmess` 和 `trojan` 会为每个 UDP 目标打开一条兼容协议 UDP 请求。

`native` 协议默认开启隧道多路复用。开启后，`tcptun client` 会维持一条到 `tcptun server` 的共享 tunnel transport 连接，并为每条被代理的 TCP 连接或 UDP relay 打开一个逻辑 stream。这样可以减少 WebSocket/HTTP/2/HTTP/3 握手次数，也更适合放在 HTTP/CDN 基础设施后面。使用 `--mux=false` 或 `"tunnel_mux": false` 可以退回到每条代理流量使用一条 tunnel transport 连接。

### nginx WebSocket 示例

服务端监听在本机回环地址：

```sh
bin/tcptun server --listen 127.0.0.1:9443 --transport ws --tunnel-path /tcptun --token change-me
```

nginx 配置 WebSocket location：

```nginx
location /tcptun {
    proxy_pass http://127.0.0.1:9443;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
}
```

客户端连接公开 HTTPS 域名：

```sh
bin/tcptun client --server-addr proxy.example.com:443 --transport ws --tunnel-path /tcptun --tls --token change-me
```

## 路由配置

运行配置和路由规则使用独立文件。默认情况下，local 模式和不带子命令的 `tcptun` 读取 `config.json`，`tcptun server` 读取 `server.json`，`tcptun client` 读取 `client.json`。路由规则默认使用 `route.json`，也可以通过 `--route-config <path>` 指定。相对配置路径会按这个顺序搜索：程序所在目录、当前工作目录、`~/.config/tcptun`。如果三个位置都不存在，写回时使用程序所在目录。传入 `--route-config ""` 可以禁用路由加载和写回。

运行配置 JSON 会加载下方示例里的字段，以及 client/server 隧道和 REALITY 字段。`gateway_ip`、`gateway_port`、`dial_timeout`、`refresh_interval`、`scan_timeout`、`scan_workers`、`buffer_size`、`verbose` 等运行参数只能通过命令行或 Go API 设置，不会从 JSON 运行配置文件加载。

运行配置示例：

```json
{
  "mode": "local",
  "listen_addr": "127.0.0.1:1080",
  "upstream_protocol": "socks5",
  "socks5_username": "",
  "socks5_password": "",
  "upstream_socks5_username": "",
  "upstream_socks5_password": "",
  "direct_probe_timeout": "500ms",
  "scan_retry_interval": "5s",
  "tunnel_protocol": "native",
  "tunnel_transport": "raw",
  "tunnel_security": "none",
  "tunnel_path": "/proxy",
  "tunnel_mux": true
}
```

路由配置示例：

```json
{
  "force_upstream": {
    "domains": ["x.com", "twitter.com"],
    "domain_regexes": ["^api\\.", "^pbs\\.twimg\\."],
    "domain_suffixes": ["x.com", "twitter.com"],
    "ips": ["8.8.8.8"],
    "ip_cidrs": ["1.1.1.0/24", "2001:4860:4860::/48"],
    "ip_ranges": ["203.0.113.0/24"]
  }
}
```

规则说明：

- `domains`：精确匹配主机名。
- `domain_regexes`：Go/RE2 正则表达式，匹配规范化后的小写主机名。
- `domain_suffixes`：匹配该域名本身及其子域名。
- `ips`：精确匹配 IP。
- `ip_cidrs` 和 `ip_ranges`：按 CIDR 前缀匹配。`ip_ranges` 是 CIDR 风格网段的别名。

程序退出前会将学习到的 TCP 直连失败目标合并写入 `route.json` 或 `--route-config` 指定的文件。失败的域名目标会追加到 `domains`，失败的 IP 目标会追加到 `ips`。如果已有精确域名、域名正则、域名后缀、精确 IP 或 IP CIDR/网段规则已经覆盖该目标，就不会重复写入。当同一可注册主域名下超过 3 个子域名出现在 `domains` 中时，程序会把该主域名提升到 `domain_suffixes`，删除已经被覆盖的 `domains` 记录，并对 `domain_suffixes` 去重排序。

## UDP 支持

UDP 通过 SOCKS5 UDP ASSOCIATE 支持。客户端先在 TCP mixed 代理端口完成协商，程序返回一个 UDP relay 地址，随后 UDP 数据包使用标准 SOCKS5 UDP 包头。内网 UDP 目标会从本地 relay 直连，非内网 UDP 目标会通过上游网关转发；在 client/server 模式下，会通过当前选择的隧道协议转发。server 仍会强制执行仅公网 IP 出站规则，目标为内网或其他非公网地址的 UDP 数据包会被直接丢弃。

## 访问日志

程序会为每条已路由的 TCP 连接和 SOCKS5 UDP 数据包输出一行访问日志。来源字段会包含识别出的代理协议和更友好的本地地址；HTTP CONNECT 记录为 `httpc`，普通 HTTP 代理流量记录为 `http`。代理流量会包含上游代理地址；直连流量会省略该中间字段。最后一个字段是 `ok` 或失败原因。

```text
httpc/localhost:53000 -> 10.207.20.78:1080 -> x.com:443 ok
http/localhost:53001 -> 192.168.1.10:80 ok
socks5-udp/localhost:53002 -> 10.207.20.78:1080 -> 8.8.8.8:53 ok
```

## 参数

```text
--buffer-size <int>         每个方向的拷贝缓冲区大小，单位字节 [默认: 32768]
-c, --config <string>       JSON 运行配置文件路径；默认按模式选择；为空表示禁用运行配置加载 [默认: "config.json"]
--route-config <string>     JSON 路由配置文件路径；为空表示禁用路由加载和写回 [默认: "route.json"]
--dial-timeout <duration>   连接上游超时时间 [默认: 5s]
--direct-probe-timeout <duration> 等待直连目标响应的超时时间，超时后走上游 [默认: 500ms]
--gateway-ip <string>       网关 IP；为空表示自动发现
-p, --gateway-port <int>    网关代理端口 [默认: 1080]
-l, --listen <string>       本机监听地址 [默认: "127.0.0.1:1080"]
--socks5-username <string>  本地 SOCKS5 用户名；用户名或密码任一非空时启用 username/password 认证
--socks5-password <string>  本地 SOCKS5 密码
--refresh-interval <duration> 检查本机 IPv4 变化的间隔；0 表示禁用刷新 [默认: 5s]
--scan-timeout <duration>   扫描 IPv4 网段时每个 IP 的探测超时 [默认: 250ms]
--scan-workers <int>        IPv4 网段扫描并发数
--upstream-protocol <string> 上游协议：socks5 或 mixed [默认: socks5]
--upstream-socks5-username <string> 上游 SOCKS5 用户名
--upstream-socks5-password <string> 上游 SOCKS5 密码
-v, --verbose               输出调试日志
```

`tcptun client` 额外支持：

```text
--server-addr <string>      自定义隧道服务端地址
--token <string>            共享 token、VLESS/VMess UUID 或 Trojan password
--tunnel-protocol <string>  隧道协议：native、vless、vmess 或 trojan [默认: native]
--tunnel-security <string>  隧道安全层：none 或 reality [默认: none]
--flow <string>             VLESS flow，例如 xtls-rprx-vision
--transport <string>        隧道承载层：raw、ws、h2 或 h3 [默认: raw]
--tunnel-path <string>      HTTP/WebSocket 隧道路由路径 [默认: /proxy]
--tls                       raw/ws/h2 transport 使用 TLS
--tls-server-name <string>  TLS server name 覆盖值
--tls-insecure              跳过 TLS 证书校验
--reality-server-name <string> REALITY serverName
--reality-fingerprint <string> REALITY uTLS fingerprint [默认: chrome]
--reality-public-key <string>  REALITY publicKey
--reality-short-id <string>    REALITY shortId 十六进制值
--reality-spider-x <string>    REALITY spiderX 路径
--mux <bool>                启用隧道多路复用 [默认: true]
```

`tcptun server` 额外支持：

```text
--token <string>            共享 token、VLESS/VMess UUID 或 Trojan password
--tunnel-protocol <string>  隧道协议：native、vless、vmess 或 trojan [默认: native]
--tunnel-security <string>  隧道安全层：none 或 reality [默认: none]
--flow <string>             VLESS flow，例如 xtls-rprx-vision
--transport <string>        隧道承载层：raw、ws、h2 或 h3 [默认: raw]
--tunnel-path <string>      HTTP/WebSocket 隧道路由路径 [默认: /proxy]
--tls-cert <string>         raw/ws/h2/h3 服务端 TLS 证书文件
--tls-key <string>          raw/ws/h2/h3 服务端 TLS 私钥文件
--reality-private-key <string> REALITY privateKey
--reality-server-names <string> 逗号分隔的 REALITY serverNames
--reality-short-ids <string>   逗号分隔的 REALITY shortIds 十六进制值
--reality-dest <string>        REALITY fallback 目标 host:port
--mux <bool>                启用隧道多路复用 [默认: true]
```

## Make 命令

```sh
make build    # 构建 bin/tcptun
make release  # 交叉编译发布二进制到 dist/
make test     # 运行测试
make fmt      # 格式化 Go 代码
make tidy     # 整理 Go 模块
make run      # 使用 Makefile 默认参数运行
make clean    # 删除构建产物和本地 Go 缓存
```

`make run` 默认使用本仓库里的 `config.json` 和 `route.json`，也支持临时覆盖参数：

```sh
make run LISTEN=127.0.0.1:1081 GATEWAY_PORT=7890
make run GATEWAY_IP=192.168.1.1
make run CONFIG=/path/to/config.json
make run ROUTE_CONFIG=/path/to/route.json
make run UPSTREAM_PROTOCOL=mixed
make run MODE=local
make run MODE=server LISTEN=0.0.0.0:9443 TOKEN=change-me
make run MODE=client SERVER_ADDR=203.0.113.10:9443 TOKEN=change-me
make run MODE=server LISTEN=0.0.0.0:9443 TUNNEL_PROTOCOL=vless TOKEN=00000000-0000-4000-8000-000000000000
make run MODE=client SERVER_ADDR=203.0.113.10:9443 TUNNEL_PROTOCOL=vless TOKEN=00000000-0000-4000-8000-000000000000
make run MODE=server LISTEN=0.0.0.0:9443 TUNNEL_PROTOCOL=vmess TRANSPORT=raw TOKEN=00000000-0000-4000-8000-000000000000
make run MODE=client SERVER_ADDR=203.0.113.10:9443 TUNNEL_PROTOCOL=vmess TRANSPORT=raw TOKEN=00000000-0000-4000-8000-000000000000
make run MODE=server LISTEN=0.0.0.0:443 TUNNEL_PROTOCOL=trojan TRANSPORT=raw TOKEN=change-me TLS_CERT=server.crt TLS_KEY=server.key
make run MODE=client SERVER_ADDR=proxy.example.com:443 TUNNEL_PROTOCOL=trojan TRANSPORT=raw TOKEN=change-me TLS=1 TLS_SERVER_NAME=proxy.example.com
make run MODE=server LISTEN=0.0.0.0:443 TUNNEL_PROTOCOL=vless TRANSPORT=raw TUNNEL_SECURITY=reality FLOW=xtls-rprx-vision TOKEN=00000000-0000-4000-8000-000000000000 REALITY_PRIVATE_KEY=REALITY_PRIVATE_KEY REALITY_SERVER_NAMES=example.com REALITY_DEST=example.com:443
make run MODE=client SERVER_ADDR=proxy.example.com:443 TUNNEL_PROTOCOL=vless TRANSPORT=raw TUNNEL_SECURITY=reality FLOW=xtls-rprx-vision TOKEN=00000000-0000-4000-8000-000000000000 REALITY_SERVER_NAME=example.com REALITY_PUBLIC_KEY=REALITY_PUBLIC_KEY REALITY_FINGERPRINT=chrome
make run MODE=server LISTEN=127.0.0.1:9443 TRANSPORT=ws TUNNEL_PATH=/tcptun TOKEN=change-me
make run MODE=client SERVER_ADDR=proxy.example.com:443 TRANSPORT=ws TUNNEL_PATH=/tcptun TLS=1 TOKEN=change-me
make run MODE=client SERVER_ADDR=proxy.example.com:443 TRANSPORT=ws MUX=false TOKEN=change-me
```

`MODE=local`、`MODE=server` 和 `MODE=client` 是 Makefile 快捷入口，会分别运行 `tcptun local`、`tcptun server` 和 `tcptun client`。

`make release` 会构建 `RELEASE_TARGETS` 中列出的目标平台，默认覆盖 Linux、macOS、Windows 的 amd64/arm64，以及 Linux arm/v7。可通过 `DIST_DIR` 或 `RELEASE_TARGETS` 覆盖输出目录或平台列表。

## 开发

运行测试：

```sh
make test
```

格式化和整理依赖：

```sh
make fmt
make tidy
```

清理生成文件：

```sh
make clean
```
