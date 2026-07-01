(function () {
  "use strict";

  const DEFAULT_CLIENT_LISTEN = "127.0.0.1:1080";
  const DEFAULT_SERVER_LISTEN = "0.0.0.0:443";
  const SUPPORTED_PROTOCOLS = new Set(["vless", "vmess", "trojan"]);
  let generatedFiles = {};
  let activeGeneratedFile = "server";
  let converterText = "";
  let currentLanguage = "en";

  const translations = {
    en: {
      "meta.title": "tcptun - High-performance TCP tunnel and mixed proxy",
      "meta.description": "tcptun is a high-performance Go TCP tunnel and mixed proxy for native, VLESS, VMess, Trojan, and REALITY/Vision client-server deployments.",
      "meta.ogDescription": "A high-performance Go TCP tunnel and mixed proxy with native, VLESS, VMess, Trojan, and REALITY/Vision support.",
      "nav.features": "Features",
      "nav.guide": "Guide",
      "nav.deploy": "Deploy",
      "nav.generator": "Generator",
      "nav.converter": "Converter",
      "nav.download": "Download",
      "hero.eyebrow": "Go TCP tunnel and mixed proxy",
      "hero.title": "Fast client-server tunneling for modern proxy deployments.",
      "hero.lede": "tcptun carries SOCKS5, HTTP CONNECT, and tunnel traffic through native, VLESS, VMess, Trojan, and REALITY/Vision modes with low overhead and production-friendly configuration.",
      "hero.download": "Download latest",
      "hero.source": "View source",
      "features.eyebrow": "Capabilities",
      "features.title": "One binary, several deployment shapes.",
      "features.mixedTitle": "Mixed local proxy",
      "features.mixedBody": "Accepts SOCKS5, SOCKS5 UDP ASSOCIATE, HTTP proxy, and HTTP CONNECT traffic from one local listener.",
      "features.protocolTitle": "Multiple tunnel protocols",
      "features.protocolBody": "Use the native protocol for full coverage, or VLESS, VMess, and Trojan for compatible TCP tunneling.",
      "features.realityTitle": "REALITY/Vision support",
      "features.realityBody": "Run VLESS over raw transport with REALITY/Vision for deployments that need realistic TLS behavior.",
      "features.transportTitle": "Efficient transports",
      "features.transportBody": "Carry tunnels over raw TCP, WebSocket, HTTP/2, or HTTP/3, with multiplexing in native mode.",
      "deploy.eyebrow": "Deploy",
      "deploy.title": "Start with generated config files.",
      "deploy.body": "Generate ready-to-edit server, client, and route configuration files, then run the matching server and local client commands.",
      "guide.eyebrow": "Usage Guide",
      "guide.title": "From a fresh server to a local proxy port.",
      "guide.body": "The fastest path is to generate matching JSON files, copy the server file to your VPS, and keep the client file on your computer.",
      "guide.step1Title": "Generate config",
      "guide.step1Body": "Create server, client, and route files with one shared token.",
      "guide.step2Title": "Run the server",
      "guide.step2Body": "On public servers, tcptun only dials public destination IPs and drops private ranges.",
      "guide.step3Title": "Run the client",
      "guide.step3Body": "The client opens a local mixed proxy endpoint for SOCKS5, HTTP proxy, and CONNECT traffic.",
      "guide.step4Title": "Use the proxy",
      "guide.step4Body": "Point your browser, app, or CLI tool at the local listener.",
      "guide.realityTitle": "REALITY/Vision",
      "guide.realityBody": "Use VLESS over raw transport with <code>tunnel_security</code> set to <code>reality</code> and <code>flow</code> set to <code>xtls-rprx-vision</code>. The server needs a private key, allowed server names, and a fallback destination; the client needs the matching public key, server name, UUID, and short ID.",
      "guide.nginxTitle": "Behind nginx",
      "guide.nginxBody": "Use WebSocket transport when placing tcptun behind a standard HTTP reverse proxy. Run the server on loopback and proxy the configured tunnel path to that local port.",
      "generator.eyebrow": "Config Generator",
      "generator.title": "Generate ready-to-run config files.",
      "generator.body": "Fill in the deployment details and generate matching <code>server.json</code>, <code>client.json</code>, and <code>route.json</code>. Tokens and REALITY keys are generated in your browser.",
      "generator.protocol": "Protocol",
      "generator.transport": "Transport",
      "generator.security": "Security",
      "generator.serverAddr": "Server addr",
      "generator.serverListen": "Server listen",
      "generator.clientListen": "Client listen",
      "generator.path": "Tunnel path",
      "generator.serverName": "Server name",
      "generator.realityDest": "REALITY dest",
      "generator.shortID": "Short ID",
      "generator.shortIDPlaceholder": "empty is allowed",
      "generator.forceCIDRs": "Force CIDRs",
      "generator.generate": "Generate",
      "generator.copy": "Copy current",
      "generator.download": "Download current",
      "generator.generatedFiles": "Generated files",
      "generator.initialOutput": "Click Generate to create config files.",
      "generator.generated": "Generated server.json, client.json, and route.json.",
      "converter.eyebrow": "Xray / V2Ray Converter",
      "converter.title": "Convert JSON configs in your browser.",
      "converter.body": "Paste an Xray or V2Ray JSON config and generate a tcptun <code>client.json</code> or <code>server.json</code>. Conversion runs locally in this page; the config never leaves your browser.",
      "converter.target": "Target",
      "converter.targetClient": "client.json from outbound",
      "converter.targetServer": "server.json from inbound",
      "converter.tag": "Tag",
      "converter.tagPlaceholder": "optional inbound/outbound tag",
      "converter.listen": "Listen",
      "converter.upstream": "Upstream",
      "converter.source": "Xray / V2Ray JSON",
      "converter.convert": "Convert",
      "converter.sample": "Load sample",
      "converter.output": "tcptun output",
      "converter.copy": "Copy",
      "converter.download": "Download",
      "converter.initialOutput": "Paste a config and click Convert.",
      "converter.converted": "Converted.",
      "download.eyebrow": "Download",
      "download.title": "Prebuilt binaries for common platforms.",
      "download.body": "Release packages include the binary, English and Chinese README files, docs, and SHA-256 checksum files.",
      "download.open": "Open releases",
      "status.copied": "Copied.",
    },
    zh: {
      "meta.title": "tcptun - 高性能 TCP 隧道和 mixed 代理",
      "meta.description": "tcptun 是一个高性能 Go TCP 隧道和 mixed 代理，支持 native、VLESS、VMess、Trojan、REALITY/Vision client-server 部署。",
      "meta.ogDescription": "高性能 Go TCP 隧道和 mixed 代理，支持 native、VLESS、VMess、Trojan 和 REALITY/Vision。",
      "nav.features": "功能",
      "nav.guide": "教程",
      "nav.deploy": "部署",
      "nav.generator": "生成配置",
      "nav.converter": "配置转换",
      "nav.download": "下载",
      "hero.eyebrow": "Go TCP 隧道和 mixed 代理",
      "hero.title": "面向现代代理部署的高速 client-server 隧道。",
      "hero.lede": "tcptun 以低开销承载 SOCKS5、HTTP CONNECT 和隧道流量，支持 native、VLESS、VMess、Trojan、REALITY/Vision，并提供适合生产环境的配置方式。",
      "hero.download": "下载最新版",
      "hero.source": "查看源码",
      "features.eyebrow": "能力",
      "features.title": "一个二进制，覆盖多种部署形态。",
      "features.mixedTitle": "本地 mixed 代理",
      "features.mixedBody": "一个本地监听端口同时接收 SOCKS5、SOCKS5 UDP ASSOCIATE、HTTP 代理和 HTTP CONNECT 流量。",
      "features.protocolTitle": "多种隧道协议",
      "features.protocolBody": "默认 native 协议覆盖能力最完整，也可以使用 VLESS、VMess、Trojan 承载兼容 TCP 隧道。",
      "features.realityTitle": "REALITY/Vision 支持",
      "features.realityBody": "使用 VLESS over raw transport 搭配 REALITY/Vision，适合需要真实 TLS 行为的部署。",
      "features.transportTitle": "高效承载层",
      "features.transportBody": "隧道可运行在 raw TCP、WebSocket、HTTP/2 或 HTTP/3 上，native 模式支持多路复用。",
      "deploy.eyebrow": "部署",
      "deploy.title": "从生成配置文件开始。",
      "deploy.body": "生成可直接编辑的 server、client 和 route 配置文件，然后分别运行匹配的服务端和本地客户端命令。",
      "guide.eyebrow": "使用教程",
      "guide.title": "从一台新服务器到本地代理端口。",
      "guide.body": "最快路径是生成匹配的 JSON 配置，把 server 文件复制到 VPS，把 client 文件留在你的电脑上。",
      "guide.step1Title": "生成配置",
      "guide.step1Body": "创建共享同一个 token 的 server、client 和 route 文件。",
      "guide.step2Title": "启动服务端",
      "guide.step2Body": "在公网服务器上，tcptun 只会拨号公网目标 IP，并直接丢弃私有网段。",
      "guide.step3Title": "启动客户端",
      "guide.step3Body": "客户端会打开一个本地 mixed 代理入口，支持 SOCKS5、HTTP 代理和 CONNECT 流量。",
      "guide.step4Title": "使用代理",
      "guide.step4Body": "把浏览器、应用或 CLI 工具指向本地监听地址。",
      "guide.realityTitle": "REALITY/Vision",
      "guide.realityBody": "使用 VLESS over raw transport，并将 <code>tunnel_security</code> 设为 <code>reality</code>、<code>flow</code> 设为 <code>xtls-rprx-vision</code>。服务端需要 private key、允许的 server names 和 fallback destination；客户端需要匹配的 public key、server name、UUID 和 short ID。",
      "guide.nginxTitle": "放在 nginx 后面",
      "guide.nginxBody": "放在标准 HTTP 反向代理后面时使用 WebSocket transport。服务端监听本机回环地址，nginx 将配置的 tunnel path 转发到这个本地端口。",
      "generator.eyebrow": "配置生成器",
      "generator.title": "生成可直接运行的配置文件。",
      "generator.body": "填写部署信息后生成匹配的 <code>server.json</code>、<code>client.json</code> 和 <code>route.json</code>。token 和 REALITY 密钥都在你的浏览器本地生成。",
      "generator.protocol": "协议",
      "generator.transport": "承载层",
      "generator.security": "安全层",
      "generator.serverAddr": "服务端地址",
      "generator.serverListen": "服务端监听",
      "generator.clientListen": "客户端监听",
      "generator.path": "隧道路由",
      "generator.serverName": "Server name",
      "generator.realityDest": "REALITY 目标",
      "generator.shortID": "Short ID",
      "generator.shortIDPlaceholder": "允许留空",
      "generator.forceCIDRs": "强制上游 CIDR",
      "generator.generate": "生成",
      "generator.copy": "复制当前",
      "generator.download": "下载当前",
      "generator.generatedFiles": "已生成文件",
      "generator.initialOutput": "点击生成配置文件。",
      "generator.generated": "已生成 server.json、client.json 和 route.json。",
      "converter.eyebrow": "Xray / V2Ray 转换器",
      "converter.title": "在浏览器里转换 JSON 配置。",
      "converter.body": "粘贴 Xray 或 V2Ray JSON 配置，生成 tcptun 的 <code>client.json</code> 或 <code>server.json</code>。转换完全在当前页面本地完成，配置不会离开你的浏览器。",
      "converter.target": "目标",
      "converter.targetClient": "从 outbound 生成 client.json",
      "converter.targetServer": "从 inbound 生成 server.json",
      "converter.tag": "Tag",
      "converter.tagPlaceholder": "可选 inbound/outbound tag",
      "converter.listen": "监听",
      "converter.upstream": "上游",
      "converter.source": "Xray / V2Ray JSON",
      "converter.convert": "转换",
      "converter.sample": "加载样例",
      "converter.output": "tcptun 输出",
      "converter.copy": "复制",
      "converter.download": "下载",
      "converter.initialOutput": "粘贴配置后点击转换。",
      "converter.converted": "已转换。",
      "download.eyebrow": "下载",
      "download.title": "常见平台的预编译二进制。",
      "download.body": "发布包包含二进制文件、中英文 README、文档和 SHA-256 校验文件。",
      "download.open": "打开 releases",
      "status.copied": "已复制。",
    },
  };

  function t(key) {
    return (translations[currentLanguage] && translations[currentLanguage][key]) || translations.en[key] || key;
  }

  function normalizeLanguage(value) {
    return String(value || "").toLowerCase().startsWith("zh") ? "zh" : "en";
  }

  function initialLanguage() {
    try {
      const saved = localStorage.getItem("tcptun_language");
      if (saved === "en" || saved === "zh") return saved;
    } catch (error) {
      // Ignore storage access errors in private or restricted browsing modes.
    }
    const languages = navigator.languages && navigator.languages.length ? navigator.languages : [navigator.language];
    return normalizeLanguage(languages[0]);
  }

  function applyTranslations() {
    document.documentElement.lang = currentLanguage === "zh" ? "zh-CN" : "en";
    document.querySelectorAll("[data-i18n]").forEach((element) => {
      element.innerHTML = t(element.dataset.i18n);
    });
    document.querySelectorAll("[data-i18n-placeholder]").forEach((element) => {
      element.setAttribute("placeholder", t(element.dataset.i18nPlaceholder));
    });
    document.querySelectorAll("[data-i18n-content]").forEach((element) => {
      element.setAttribute("content", t(element.dataset.i18nContent));
    });
    document.querySelectorAll("[data-i18n-aria-label]").forEach((element) => {
      element.setAttribute("aria-label", t(element.dataset.i18nAriaLabel));
    });
    document.title = t("meta.title");
    document.querySelectorAll("[data-lang-option]").forEach((button) => {
      const active = button.dataset.langOption === currentLanguage;
      button.classList.toggle("active", active);
      button.setAttribute("aria-pressed", active ? "true" : "false");
    });
    refreshEmptyOutputs();
  }

  function setLanguage(language) {
    currentLanguage = language === "zh" ? "zh" : "en";
    try {
      localStorage.setItem("tcptun_language", currentLanguage);
    } catch (error) {
      // Ignore storage access errors.
    }
    applyTranslations();
  }

  function initI18n() {
    currentLanguage = initialLanguage();
    document.querySelectorAll("[data-lang-option]").forEach((button) => {
      button.addEventListener("click", () => setLanguage(button.dataset.langOption));
    });
    applyTranslations();
  }

  function refreshEmptyOutputs() {
    const generatorOutput = document.getElementById("generator-output");
    if (generatorOutput && !generatedFiles[activeGeneratedFile]) {
      setCode(generatorOutput, t("generator.initialOutput"));
    }
    const converterOutput = document.getElementById("converter-output");
    if (converterOutput && !converterText) {
      setCode(converterOutput, t("converter.initialOutput"));
    }
  }

  function normalizePath(value) {
    const trimmed = String(value || "").trim();
    if (!trimmed) return "";
    return trimmed.startsWith("/") ? trimmed : `/${trimmed}`;
  }

  function cleanObject(value) {
    if (Array.isArray(value)) {
      return value.map(cleanObject).filter((item) => item !== undefined);
    }
    if (value && typeof value === "object") {
      const output = {};
      for (const [key, item] of Object.entries(value)) {
        const cleaned = cleanObject(item);
        if (cleaned === undefined) continue;
        if (Array.isArray(cleaned) && cleaned.length === 0) continue;
        output[key] = cleaned;
      }
      return output;
    }
    if (value === "" || value === false || value === null || value === undefined) {
      return undefined;
    }
    return value;
  }

  function randomBytes(size) {
    const bytes = new Uint8Array(size);
    crypto.getRandomValues(bytes);
    return bytes;
  }

  function hexToken(size) {
    return Array.from(randomBytes(size), (byte) => byte.toString(16).padStart(2, "0")).join("");
  }

  function uuidV4() {
    const bytes = randomBytes(16);
    bytes[6] = (bytes[6] & 0x0f) | 0x40;
    bytes[8] = (bytes[8] & 0x3f) | 0x80;
    const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
    return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
  }

  function base64URL(bytes) {
    let binary = "";
    for (const byte of bytes) binary += String.fromCharCode(byte);
    return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/g, "");
  }

  async function realityKeyPair() {
    if (!crypto.subtle) {
      throw new Error("This browser does not support WebCrypto X25519 key generation.");
    }
    const keyPair = await crypto.subtle.generateKey({ name: "X25519" }, true, ["deriveBits"]);
    const privateJWK = await crypto.subtle.exportKey("jwk", keyPair.privateKey);
    const publicJWK = await crypto.subtle.exportKey("jwk", keyPair.publicKey);
    if (!privateJWK.d || !publicJWK.x) {
      throw new Error("Failed to export generated REALITY keys.");
    }
    return {
      privateKey: privateJWK.d,
      publicKey: publicJWK.x,
    };
  }

  function tokenForProtocol(protocol) {
    if (protocol === "vless" || protocol === "vmess") return uuidV4();
    return hexToken(32);
  }

  async function generateConfigs(form) {
    const protocol = form.protocol;
    const transport = form.transport;
    const security = form.security;
    if (security === "reality" && protocol !== "vless") {
      throw new Error("REALITY config generation requires VLESS.");
    }
    if (security === "reality" && transport !== "raw") {
      throw new Error("REALITY config generation requires raw transport.");
    }

    const token = tokenForProtocol(protocol);
    const tunnelPath = normalizePath(form.path);
    const server = {
      mode: "server",
      listen_addr: form.serverListen,
      token,
      tunnel_protocol: protocol,
      tunnel_transport: transport,
      tunnel_path: tunnelPath,
    };
    const client = {
      mode: "client",
      listen_addr: form.clientListen,
      server_addr: form.serverAddr,
      token,
      tunnel_protocol: protocol,
      tunnel_transport: transport,
      tunnel_path: tunnelPath,
      upstream_protocol: "socks5",
    };

    if (security === "tls" || transport === "h3") {
      server.tunnel_tls_cert = "server.crt";
      server.tunnel_tls_key = "server.key";
      if (transport !== "h3") {
        client.tunnel_tls = true;
      }
      client.tunnel_tls_server_name = form.serverName;
    }

    if (security === "reality") {
      const keys = await realityKeyPair();
      server.tunnel_security = "reality";
      server.tunnel_flow = "xtls-rprx-vision";
      server.reality_private_key = keys.privateKey;
      server.reality_server_names = [form.serverName];
      server.reality_dest = form.realityDest;
      if (form.shortID) server.reality_short_ids = [form.shortID];

      client.tunnel_security = "reality";
      client.tunnel_flow = "xtls-rprx-vision";
      client.reality_server_name = form.serverName;
      client.reality_fingerprint = "chrome";
      client.reality_public_key = keys.publicKey;
      client.reality_spider_x = "/";
      if (form.shortID) client.reality_short_id = form.shortID;
    }

    const route = {
      force_upstream: {
        domains: [],
        domain_regexes: [],
        domain_suffixes: [],
        ips: [],
        ip_cidrs: commaList(form.forceCIDRs),
        ip_ranges: [],
      },
    };

    return {
      server: cleanObject(server),
      client: cleanObject(client),
      route,
    };
  }

  function commaList(value) {
    return String(value || "")
      .split(",")
      .map((item) => item.trim())
      .filter(Boolean);
  }

  function convertConfig(source, options) {
    const target = options.target || "client";
    if (target === "client") return convertClient(source, options);
    if (target === "server") return convertServer(source, options);
    throw new Error(`Unsupported target: ${target}`);
  }

  function sampleConfig(target) {
    if (target === "server") {
      return {
        inbounds: [
          {
            tag: "proxy",
            listen: "0.0.0.0",
            port: 443,
            protocol: "vless",
            settings: {
              clients: [{ id: uuidV4(), flow: "xtls-rprx-vision", encryption: "none" }],
              decryption: "none",
            },
            streamSettings: {
              network: "tcp",
              security: "reality",
              realitySettings: {
                dest: "example.com:443",
                serverNames: ["example.com"],
                privateKey: "REALITY_PRIVATE_KEY",
                shortIds: [""],
              },
            },
          },
        ],
      };
    }
    return {
      outbounds: [
        {
          tag: "proxy",
          protocol: "vless",
          settings: {
            vnext: [
              {
                address: "proxy.example.com",
                port: 443,
                users: [{ id: uuidV4(), flow: "xtls-rprx-vision", encryption: "none" }],
              },
            ],
          },
          streamSettings: {
            network: "tcp",
            security: "reality",
            realitySettings: {
              serverName: "example.com",
              fingerprint: "chrome",
              publicKey: "REALITY_PUBLIC_KEY",
              shortId: "",
              spiderX: "/",
            },
          },
        },
      ],
    };
  }

  function convertClient(source, options) {
    const outbound = selectEndpoint(source.outbounds, options.tag, "outbound");
    const protocol = normalizeProtocol(outbound.protocol);
    const server = outboundServer(protocol, outbound);
    const stream = outbound.streamSettings || {};
    const transport = mapTransport(stream);
    const output = {
      mode: "client",
      listen_addr: options.listen || DEFAULT_CLIENT_LISTEN,
      server_addr: joinHostPort(server.address, server.port),
      token: server.token,
      tunnel_protocol: protocol,
      tunnel_transport: transport.transport,
      tunnel_path: transport.path,
      tunnel_flow: server.flow,
      upstream_protocol: options.upstreamProtocol || "socks5",
    };
    applyClientSecurity(output, stream);
    return cleanObject(output);
  }

  function convertServer(source, options) {
    const inbound = selectEndpoint(source.inbounds, options.tag, "inbound");
    const protocol = normalizeProtocol(inbound.protocol);
    const auth = inboundAuth(protocol, inbound);
    const stream = inbound.streamSettings || {};
    const transport = mapTransport(stream);
    const output = {
      mode: "server",
      listen_addr: serverListen(inbound, options.listen),
      token: auth.token,
      tunnel_protocol: protocol,
      tunnel_transport: transport.transport,
      tunnel_path: transport.path,
      tunnel_flow: auth.flow,
    };
    applyServerSecurity(output, stream);
    return cleanObject(output);
  }

  function selectEndpoint(endpoints, tag, name) {
    if (!Array.isArray(endpoints) || endpoints.length === 0) {
      throw new Error(`The config has no ${name}s.`);
    }
    const wanted = String(tag || "").trim();
    if (wanted) {
      const found = endpoints.find((endpoint) => endpoint.tag === wanted);
      if (!found) throw new Error(`${name} tag "${wanted}" was not found.`);
      return found;
    }
    const found = endpoints.find((endpoint) => SUPPORTED_PROTOCOLS.has(String(endpoint.protocol || "").toLowerCase()));
    if (!found) throw new Error(`No supported ${name} found. Supported protocols: vless, vmess, trojan.`);
    return found;
  }

  function normalizeProtocol(value) {
    const protocol = String(value || "").trim().toLowerCase();
    if (!SUPPORTED_PROTOCOLS.has(protocol)) throw new Error(`Unsupported protocol: ${value}`);
    return protocol;
  }

  function outboundServer(protocol, outbound) {
    const settings = outbound.settings || {};
    if (protocol === "trojan") {
      const server = first(settings.servers, "trojan outbound server");
      if (!server.password) throw new Error("Trojan outbound password is empty.");
      return {
        address: server.address,
        port: server.port,
        token: server.password,
        flow: server.flow || "",
      };
    }
    const server = first(settings.vnext, `${protocol} outbound vnext server`);
    const user = first(server.users, `${protocol} outbound user`);
    if (!user.id) throw new Error(`${protocol} outbound user id is empty.`);
    return {
      address: server.address,
      port: server.port,
      token: user.id,
      flow: user.flow || "",
    };
  }

  function inboundAuth(protocol, inbound) {
    const settings = inbound.settings || {};
    const client = first(settings.clients, `${protocol} inbound client`);
    if (protocol === "trojan") {
      if (!client.password) throw new Error("Trojan inbound client password is empty.");
      return { token: client.password, flow: client.flow || "" };
    }
    if (!client.id) throw new Error(`${protocol} inbound client id is empty.`);
    return { token: client.id, flow: client.flow || "" };
  }

  function first(values, label) {
    if (!Array.isArray(values) || values.length === 0) throw new Error(`Missing ${label}.`);
    return values[0];
  }

  function joinHostPort(address, port) {
    const host = String(address || "").trim();
    const parsedPort = Number(port);
    if (!host) throw new Error("Server address is empty.");
    if (!Number.isInteger(parsedPort) || parsedPort <= 0 || parsedPort > 65535) {
      throw new Error(`Invalid server port: ${port}`);
    }
    if (host.includes(":") && !host.startsWith("[")) return `[${host}]:${parsedPort}`;
    return `${host}:${parsedPort}`;
  }

  function serverListen(inbound, override) {
    const trimmed = String(override || "").trim();
    if (trimmed && trimmed !== DEFAULT_CLIENT_LISTEN) return trimmed;
    const host = inbound.listen || "0.0.0.0";
    if (!inbound.port) return DEFAULT_SERVER_LISTEN;
    return joinHostPort(host, inbound.port);
  }

  function mapTransport(stream) {
    const network = String(stream.network || "tcp").trim().toLowerCase();
    if (network === "tcp" || network === "raw") return { transport: "raw", path: "" };
    if (network === "ws" || network === "websocket") {
      return { transport: "ws", path: normalizePath(stream.wsSettings && stream.wsSettings.path) };
    }
    if (network === "http" || network === "h2") {
      return { transport: "h2", path: normalizePath(stream.httpSettings && stream.httpSettings.path) };
    }
    if (network === "quic") throw new Error("Xray QUIC transport is not the same as tcptun HTTP/3.");
    throw new Error(`Unsupported Xray/V2Ray network: ${stream.network}`);
  }

  function applyClientSecurity(output, stream) {
    const security = String(stream.security || "none").trim().toLowerCase();
    if (security === "none") return;
    if (security === "tls") {
      const tls = stream.tlsSettings || {};
      output.tunnel_tls = true;
      output.tunnel_tls_server_name = tls.serverName || "";
      output.tunnel_tls_insecure = Boolean(tls.allowInsecure);
      return;
    }
    if (security === "reality") {
      const reality = stream.realitySettings || {};
      if (!reality.serverName) throw new Error("REALITY outbound requires realitySettings.serverName.");
      if (!reality.publicKey) throw new Error("REALITY outbound requires realitySettings.publicKey.");
      output.tunnel_security = "reality";
      output.reality_server_name = reality.serverName;
      output.reality_fingerprint = reality.fingerprint || "chrome";
      output.reality_public_key = reality.publicKey;
      output.reality_short_id = reality.shortId || "";
      output.reality_spider_x = reality.spiderX || "";
      return;
    }
    throw new Error(`Unsupported stream security: ${stream.security}`);
  }

  function applyServerSecurity(output, stream) {
    const security = String(stream.security || "none").trim().toLowerCase();
    if (security === "none") return;
    if (security === "tls") {
      const certificate = first(stream.tlsSettings && stream.tlsSettings.certificates, "TLS certificate");
      output.tunnel_tls_cert = certificate.certificateFile || "";
      output.tunnel_tls_key = certificate.keyFile || "";
      if (!output.tunnel_tls_cert || !output.tunnel_tls_key) {
        throw new Error("TLS inbound certificateFile and keyFile are required.");
      }
      return;
    }
    if (security === "reality") {
      const reality = stream.realitySettings || {};
      if (!reality.privateKey) throw new Error("REALITY inbound requires realitySettings.privateKey.");
      if (!Array.isArray(reality.serverNames) || reality.serverNames.length === 0) {
        throw new Error("REALITY inbound requires realitySettings.serverNames.");
      }
      if (!reality.dest) throw new Error("REALITY inbound requires realitySettings.dest.");
      output.tunnel_security = "reality";
      output.reality_private_key = reality.privateKey;
      output.reality_server_names = reality.serverNames;
      output.reality_short_ids = reality.shortIds || [];
      output.reality_dest = reality.dest;
      return;
    }
    throw new Error(`Unsupported stream security: ${stream.security}`);
  }

  function pretty(value) {
    return JSON.stringify(value, null, 2);
  }

  function setStatus(element, message, kind) {
    if (!element) return;
    element.textContent = message || "";
    element.classList.remove("ok", "error");
    if (kind) element.classList.add(kind);
  }

  function setCode(element, text) {
    if (!element) return;
    element.textContent = text;
  }

  async function copyText(text, statusElement) {
    if (!text) return;
    await navigator.clipboard.writeText(text);
    setStatus(statusElement, t("status.copied"), "ok");
  }

  function downloadText(filename, text) {
    const blob = new Blob([text], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = filename;
    link.click();
    URL.revokeObjectURL(url);
  }

  function initGenerator() {
    const output = document.getElementById("generator-output");
    const status = document.getElementById("generator-status");
    const tabs = Array.from(document.querySelectorAll("[data-generated-file]"));
    const currentText = () => (generatedFiles[activeGeneratedFile] ? pretty(generatedFiles[activeGeneratedFile]) : "");

    function renderActive() {
      const text = currentText();
      setCode(output, text || t("generator.initialOutput"));
      tabs.forEach((tab) => {
        tab.classList.toggle("active", tab.dataset.generatedFile === activeGeneratedFile);
      });
    }

    tabs.forEach((tab) => {
      tab.addEventListener("click", () => {
        activeGeneratedFile = tab.dataset.generatedFile;
        renderActive();
      });
    });

    document.getElementById("generate-button").addEventListener("click", async () => {
      try {
        generatedFiles = await generateConfigs({
          protocol: document.getElementById("gen-protocol").value,
          transport: document.getElementById("gen-transport").value,
          security: document.getElementById("gen-security").value,
          serverAddr: document.getElementById("gen-server-addr").value.trim(),
          serverListen: document.getElementById("gen-server-listen").value.trim(),
          clientListen: document.getElementById("gen-client-listen").value.trim(),
          path: document.getElementById("gen-path").value.trim(),
          serverName: document.getElementById("gen-server-name").value.trim(),
          realityDest: document.getElementById("gen-reality-dest").value.trim(),
          shortID: document.getElementById("gen-short-id").value.trim(),
          forceCIDRs: document.getElementById("gen-force-cidrs").value.trim(),
        });
        activeGeneratedFile = "server";
        renderActive();
        setStatus(status, t("generator.generated"), "ok");
      } catch (error) {
        setStatus(status, error.message, "error");
      }
    });

    document.getElementById("generator-copy-button").addEventListener("click", () => {
      copyText(currentText(), status).catch((error) => setStatus(status, error.message, "error"));
    });
    document.getElementById("generator-download-button").addEventListener("click", () => {
      const text = currentText();
      if (!text) return;
      downloadText(`${activeGeneratedFile}.json`, text);
    });
  }

  function initConverter() {
    const target = document.getElementById("target");
    const listen = document.getElementById("listen");
    const upstreamRow = document.getElementById("upstream-row");
    const source = document.getElementById("source-config");
    const output = document.getElementById("converter-output");
    const status = document.getElementById("converter-status");

    function syncTarget() {
      const isClient = target.value === "client";
      upstreamRow.style.display = isClient ? "grid" : "none";
      if (isClient && listen.value === DEFAULT_SERVER_LISTEN) listen.value = DEFAULT_CLIENT_LISTEN;
      if (!isClient && listen.value === DEFAULT_CLIENT_LISTEN) listen.value = "";
    }

    target.addEventListener("change", syncTarget);
    syncTarget();

    document.getElementById("sample-button").addEventListener("click", () => {
      source.value = pretty(sampleConfig(target.value));
      setStatus(status, "", "");
    });

    document.getElementById("convert-button").addEventListener("click", () => {
      try {
        const parsed = JSON.parse(source.value);
        const converted = convertConfig(parsed, {
          target: target.value,
          tag: document.getElementById("tag").value,
          listen: listen.value.trim(),
          upstreamProtocol: document.getElementById("upstream").value,
        });
        converterText = pretty(converted);
        setCode(output, converterText);
        setStatus(status, t("converter.converted"), "ok");
      } catch (error) {
        setStatus(status, error.message, "error");
      }
    });

    document.getElementById("copy-button").addEventListener("click", () => {
      copyText(converterText, status).catch((error) => setStatus(status, error.message, "error"));
    });
    document.getElementById("download-button").addEventListener("click", () => {
      if (!converterText) return;
      downloadText(target.value === "server" ? "server.json" : "client.json", converterText);
    });
  }

  if (typeof document !== "undefined") {
    document.addEventListener("DOMContentLoaded", () => {
      initI18n();
      initGenerator();
      initConverter();
      refreshEmptyOutputs();
    });
  }

  if (typeof module !== "undefined") {
    module.exports = {
      convertConfig,
      generateConfigs,
      sampleConfig,
      normalizeLanguage,
      uuidV4,
      realityKeyPair,
    };
  }
})();
