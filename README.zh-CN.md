# Proxy

本地 mixed 代理转发程序，使用 Go 编写。

这个工具会在本机打开一个 mixed 代理端口，并把需要走上游的流量转发到网关。上游协议可配置，默认使用 SOCKS5。它偏性能优先设计：连接转发使用复用缓冲区，HTTP 和 SOCKS5 请求只解析到足够完成直连/上游路由选择。

[English](README.md) | 简体中文

## 功能

- 默认监听本机 `127.0.0.1:1080`。
- 默认转发到网关代理端口 `1080`。
- 本地支持 mixed 代理流量，包括 SOCKS5、HTTP 代理、HTTP CONNECT。
- 默认使用 SOCKS5 作为上游协议，也支持 `mixed` 上游模式。
- 支持 SOCKS5 UDP ASSOCIATE，可转发 UDP 流量。
- 在终端输出紧凑访问日志；直连日志会省略代理字段。
- 自动发现默认网关 IP。
- 仅在本机存在内网 IPv4 地址时，检测发现到的网关端口是否可连通。
- 如果默认网关不可连通，自动扫描本机所在的内网 IPv4 网段。
- 运行期间定时刷新可用上游，网络变化后新连接会使用新的目标地址。
- 对私有地址、回环地址、链路本地地址、`localhost` 和 `.local` 目标直连，不转发到上游。
- TCP 目标优先直连；如果直连失败，会记住该目标，后续连接直接走上游。
- 支持通过 `config.json` 配置强制走上游规则，可按精确域名、域名前缀、域名后缀、精确 IP 和 IP CIDR/网段匹配。
- 程序退出前会把学习到的直连失败目标写回 `config.json`；文件不存在会自动创建，已有规则会去重。
- 命令行基于 `pkg.gostartkit.com/cmd v0.2.1`。

## 环境要求

- Go 1.24 或更新版本。

## 构建

```sh
make build
```

构建产物路径：

```text
bin/proxy
```

也可以直接使用 Go 构建：

```sh
go build -trimpath -ldflags "-s -w" -o bin/proxy .
```

## 运行

使用自动网关发现启动：

```sh
make run
```

或运行已构建的二进制：

```sh
bin/proxy
```

修改本机监听端口：

```sh
bin/proxy --listen 127.0.0.1:1081
```

指定已知网关 IP：

```sh
bin/proxy --gateway-ip 192.168.1.1
```

指定网关代理端口：

```sh
bin/proxy --gateway-port 7890
```

使用 mixed 上游网关，而不是默认 SOCKS5 上游：

```sh
bin/proxy --upstream-protocol mixed
```

指定路由配置文件：

```sh
bin/proxy --config ./config.json
```

## 网关发现逻辑

未设置 `--gateway-ip` 时，启动流程如下：

1. 检查本机是否存在内网 IPv4 地址。
2. 如果存在，自动发现系统默认网关 IP。
3. 尝试连接 `<网关IP>:<gateway-port>`。
4. 如果连接失败，扫描本机所在的内网 IPv4 网段，寻找打开了 `<gateway-port>` 的主机。
5. 使用第一个可连通的地址作为上游代理。

如果本机没有内网 IPv4 地址，会跳过自动网关探测和本地 IPv4 网段扫描。此时请显式设置 `--gateway-ip`。

手动设置 `--gateway-ip` 时，不会扫描网段，会直接使用该 IP。

程序运行期间会按 `--refresh-interval` 定时检查本机 IPv4 地址集合。只有本地 IPv4 地址发生变化时，才会重新发现网关或扫描本地网段。已有连接继续使用当前上游，新连接会使用变化后刷新出的目标地址。

## 内网地址直连

对于 SOCKS5、SOCKS5 UDP ASSOCIATE、HTTP CONNECT 和 HTTP 代理请求，程序会解析请求目标。`config.json` 中的强制走上游规则优先级最高。否则，TCP 目标会优先直连；如果直连失败，会记住该目标为仅走上游，后续连接跳过直连尝试。UDP 目标保持保守规则：内网目标直连，其他目标走上游。

## 上游协议

上游协议可以通过 `--upstream-protocol` 设置，也可以写在 `config.json` 顶层的 `upstream_protocol` 字段里。支持的值是 `socks5` 和 `mixed`，默认是 `socks5`。

在 `socks5` 模式下，已解析出目标的 SOCKS5 和 HTTP 代理流量会转换成 SOCKS5 后再发往上游。未知 mixed 流量因为无法解析目标，会被拒绝。

在 `mixed` 模式下，HTTP 代理流量和未知 mixed 流量会原样发往网关。SOCKS5 TCP 和 UDP 仍使用 SOCKS5 协商，因此上游 mixed 端口需要支持 SOCKS5。

## 路由配置

程序默认读取可执行文件所在目录下的 `config.json`。相对路径形式的 `--config` 会按可执行文件所在目录解析，绝对路径会原样使用。仓库自带的 `config.json` 已经将 `x.com`、`twitter.com` 及相关子域名设置为强制走上游。如果该文件不存在，会先按无自定义规则运行，并在退出前发现新的直连失败目标时自动创建。可以使用 `--config <path>` 指定其他文件，或使用 `--config ""` 禁用配置加载和写回。

示例：

```json
{
  "upstream_protocol": "socks5",
  "force_upstream": {
    "domains": ["x.com", "twitter.com"],
    "domain_prefixes": ["api.", "pbs.twimg."],
    "domain_suffixes": ["x.com", "twitter.com"],
    "ips": ["8.8.8.8"],
    "ip_cidrs": ["1.1.1.0/24", "2001:4860:4860::/48"],
    "ip_ranges": ["203.0.113.0/24"]
  }
}
```

规则说明：

- `domains`：精确匹配主机名。
- `domain_prefixes`：匹配以指定值开头的主机名。
- `domain_suffixes`：匹配该域名本身及其子域名。
- `ips`：精确匹配 IP。
- `ip_cidrs` 和 `ip_ranges`：按 CIDR 前缀匹配。`ip_ranges` 是 CIDR 风格网段的别名。

程序退出前会将学习到的 TCP 直连失败目标合并写入该文件。失败的域名目标会追加到 `domains`，失败的 IP 目标会追加到 `ips`。如果已有精确域名、域名前缀、域名后缀、精确 IP 或 IP CIDR/网段规则已经覆盖该目标，就不会重复写入。

## UDP 支持

UDP 通过 SOCKS5 UDP ASSOCIATE 支持。客户端先在 TCP mixed 代理端口完成协商，程序返回一个 UDP relay 地址，随后 UDP 数据包使用标准 SOCKS5 UDP 包头。内网 UDP 目标会从本地 relay 直连，非内网 UDP 目标会通过上游网关的 SOCKS5 UDP 能力转发。

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
-c, --config <string>       JSON 路由配置文件路径；为空表示禁用配置加载 [默认: "config.json"]
--dial-timeout <duration>   连接上游超时时间 [默认: 5s]
--gateway-ip <string>       网关 IP；为空表示自动发现
-p, --gateway-port <int>    网关代理端口 [默认: 1080]
-l, --listen <string>       本机监听地址 [默认: "127.0.0.1:1080"]
--refresh-interval <duration> 检查本机 IPv4 变化的间隔；0 表示禁用刷新 [默认: 5s]
--scan-timeout <duration>   扫描 IPv4 网段时每个 IP 的探测超时 [默认: 250ms]
--scan-workers <int>        IPv4 网段扫描并发数
--upstream-protocol <string> 上游协议：socks5 或 mixed [默认: socks5]
-v, --verbose               输出调试日志
```

## Make 命令

```sh
make build    # 构建 bin/proxy
make test     # 运行测试
make fmt      # 格式化 Go 代码
make tidy     # 整理 Go 模块
make run      # 使用 Makefile 默认参数运行
make clean    # 删除构建产物和本地 Go 缓存
```

`make run` 默认使用本仓库里的 `config.json`，也支持临时覆盖参数：

```sh
make run LISTEN=127.0.0.1:1081 GATEWAY_PORT=7890
make run GATEWAY_IP=192.168.1.1
make run CONFIG=/path/to/config.json
make run UPSTREAM_PROTOCOL=mixed
```

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
