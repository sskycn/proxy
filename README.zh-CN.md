# Proxy

本地 mixed 代理转发程序，使用 Go 编写。

这个工具会在本机打开一个 mixed 代理端口，并把接收到的每条 TCP 连接原样转发到网关上的 mixed 代理端口。它偏性能优先设计：转发热路径不解析、不改写协议数据，并使用复用缓冲区进行连接拷贝。

[English](README.md) | 简体中文

## 功能

- 默认监听本机 `127.0.0.1:1080`。
- 默认转发到网关 mixed 代理端口 `1080`。
- 当网关端口支持 mixed 协议时，可承载 SOCKS5、HTTP 代理、HTTP CONNECT 等流量。
- 自动发现默认网关 IP。
- 检测发现到的网关端口是否可连通。
- 如果默认网关不可连通，自动扫描本机所在 IPv4 网段。
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

指定网关 mixed 端口：

```sh
bin/proxy --gateway-port 7890
```

## 网关发现逻辑

未设置 `--gateway-ip` 时，启动流程如下：

1. 自动发现系统默认网关 IP。
2. 尝试连接 `<网关IP>:<gateway-port>`。
3. 如果连接失败，扫描本机所在 IPv4 网段，寻找打开了 `<gateway-port>` 的主机。
4. 使用第一个可连通的地址作为上游 mixed 代理。

手动设置 `--gateway-ip` 时，不会扫描网段，会直接使用该 IP。

## 参数

```text
--buffer-size <int>         每个方向的拷贝缓冲区大小，单位字节 [默认: 32768]
--dial-timeout <duration>   连接上游超时时间 [默认: 5s]
--gateway-ip <string>       网关 IP；为空表示自动发现
-p, --gateway-port <int>    网关 mixed 代理端口 [默认: 1080]
-l, --listen <string>       本机监听地址 [默认: "127.0.0.1:1080"]
--scan-timeout <duration>   扫描 IPv4 网段时每个 IP 的探测超时 [默认: 250ms]
--scan-workers <int>        IPv4 网段扫描并发数
-v, --verbose               输出连接日志
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

`make run` 支持临时覆盖参数：

```sh
make run LISTEN=127.0.0.1:1081 GATEWAY_PORT=7890
make run GATEWAY_IP=192.168.1.1
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
