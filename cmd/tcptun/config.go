package main

import (
	"bufio"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"sskycn/tcptun"

	"pkg.gostartkit.com/cmd"
)

const (
	configTargetBoth   = "both"
	configTargetServer = "server"
	configTargetClient = "client"
)

type generatedRouteConfig struct {
	Mode                   string   `json:"mode,omitempty"`
	ListenAddr             string   `json:"listen_addr,omitempty"`
	ListenAddrs            []string `json:"listen_addrs,omitempty"`
	ServerAddr             string   `json:"server_addr,omitempty"`
	Token                  string   `json:"token,omitempty"`
	TunnelProtocol         string   `json:"tunnel_protocol,omitempty"`
	TunnelTransport        string   `json:"tunnel_transport,omitempty"`
	TunnelPath             string   `json:"tunnel_path,omitempty"`
	TunnelTLS              bool     `json:"tunnel_tls,omitempty"`
	TunnelTLSCert          string   `json:"tunnel_tls_cert,omitempty"`
	TunnelTLSKey           string   `json:"tunnel_tls_key,omitempty"`
	TunnelTLSServerName    string   `json:"tunnel_tls_server_name,omitempty"`
	TunnelTLSInsecure      bool     `json:"tunnel_tls_insecure,omitempty"`
	TunnelSecurity         string   `json:"tunnel_security,omitempty"`
	TunnelFlow             string   `json:"tunnel_flow,omitempty"`
	RealityServerName      string   `json:"reality_server_name,omitempty"`
	RealityServerNames     []string `json:"reality_server_names,omitempty"`
	RealityFingerprint     string   `json:"reality_fingerprint,omitempty"`
	RealityPublicKey       string   `json:"reality_public_key,omitempty"`
	RealityPrivateKey      string   `json:"reality_private_key,omitempty"`
	RealityShortID         string   `json:"reality_short_id,omitempty"`
	RealityShortIDs        []string `json:"reality_short_ids,omitempty"`
	RealityDest            string   `json:"reality_dest,omitempty"`
	RealitySpiderX         string   `json:"reality_spider_x,omitempty"`
	TunnelMux              *bool    `json:"tunnel_mux,omitempty"`
	UpstreamProtocol       string   `json:"upstream_protocol,omitempty"`
	SOCKS5Username         string   `json:"socks5_username,omitempty"`
	SOCKS5Password         string   `json:"socks5_password,omitempty"`
	UpstreamSOCKS5Username string   `json:"upstream_socks5_username,omitempty"`
	UpstreamSOCKS5Password string   `json:"upstream_socks5_password,omitempty"`
}

type generatedRouteRulesConfig struct {
	ForceUpstream generatedForceUpstreamConfig `json:"force_upstream"`
}

type generatedForceUpstreamConfig struct {
	Domains        []string `json:"domains"`
	DomainRegexes  []string `json:"domain_regexes"`
	DomainSuffixes []string `json:"domain_suffixes"`
	IPCIDRs        []string `json:"ip_cidrs"`
	IPRanges       []string `json:"ip_ranges"`
	IPs            []string `json:"ips"`
}

type generateConfigOptions struct {
	target                 string
	protocol               string
	transport              string
	token                  string
	outDir                 string
	serverOutput           string
	clientOutput           string
	routeOutput            string
	serverListen           string
	clientListen           string
	serverAddr             string
	tunnelPath             string
	tunnelTLS              bool
	tunnelTLSCert          string
	tunnelTLSKey           string
	tunnelTLSServerName    string
	tunnelTLSInsecure      bool
	tunnelSecurity         string
	tunnelFlow             string
	realityServerName      string
	realityServerNames     string
	realityFingerprint     string
	realityPublicKey       string
	realityPrivateKey      string
	realityShortID         string
	realityShortIDs        string
	realityDest            string
	realitySpiderX         string
	tunnelMux              string
	upstreamProtocol       string
	socks5Username         string
	socks5Password         string
	upstreamSOCKS5Username string
	upstreamSOCKS5Password string
	forceCIDRs             string
	overwrite              bool
}

func buildConfigCommand() *cmd.Command {
	opts := generateConfigOptions{
		target:       configTargetBoth,
		protocol:     tcptun.TunnelProtocolNative,
		transport:    tcptun.TunnelTransportRaw,
		outDir:       ".",
		serverOutput: "server.json",
		clientOutput: "client.json",
		routeOutput:  "route.json",
		serverListen: "0.0.0.0:9443",
		clientListen: tcptun.DefaultConfig().ListenAddr,
		serverAddr:   "127.0.0.1:9443",
		tunnelPath:   "/proxy",
	}
	return &cmd.Command{
		Name:      "config",
		Aliases:   []string{"cfg", "gen"},
		UsageLine: "tcptun config [flags]",
		Short:     "generate server and client config files",
		Examples: []string{
			"tcptun config --protocol native",
			"tcptun config --protocol vless --server-addr proxy.example.com:9443",
			"tcptun config --protocol trojan --transport raw --tls --tls-cert server.crt --tls-key server.key --tls-server-name proxy.example.com",
			"tcptun config --target client --output client.json --server-addr proxy.example.com:9443",
		},
		SetFlags: func(f *cmd.FlagSet) {
			f.StringVar(&opts.target, "target", opts.target, "config target: both, server, or client", "")
			f.StringVar(&opts.protocol, "protocol", opts.protocol, "tunnel protocol: native, vless, vmess, or trojan", "")
			f.StringVar(&opts.transport, "transport", opts.transport, "tunnel transport: raw, ws, h2, or h3", "")
			f.StringVar(&opts.token, "token", opts.token, "shared token, VLESS/VMess UUID, or Trojan password; generated when empty", "")
			f.StringVar(&opts.outDir, "out-dir", opts.outDir, "directory for generated config files", "")
			f.StringVar(&opts.serverOutput, "server-output", opts.serverOutput, "server config output filename or path", "")
			f.StringVar(&opts.clientOutput, "client-output", opts.clientOutput, "client config output filename or path", "")
			f.StringVar(&opts.routeOutput, "route-output", opts.routeOutput, "route config output filename or path", "")
			f.StringVar(&opts.serverOutput, "output", opts.serverOutput, "single output path when --target is server or client", "o")
			f.StringVar(&opts.serverListen, "server-listen", opts.serverListen, "server listen address written to server config", "")
			f.StringVar(&opts.clientListen, "client-listen", opts.clientListen, "client local listen address written to client config", "")
			f.StringVar(&opts.serverAddr, "server-addr", opts.serverAddr, "server address written to client config", "")
			f.StringVar(&opts.tunnelPath, "tunnel-path", opts.tunnelPath, "HTTP/WebSocket tunnel path", "")
			f.BoolVar(&opts.tunnelTLS, "tls", opts.tunnelTLS, "enable TLS for client config and write cert/key paths to server config when provided", "")
			f.StringVar(&opts.tunnelTLSCert, "tls-cert", opts.tunnelTLSCert, "server TLS certificate file path", "")
			f.StringVar(&opts.tunnelTLSKey, "tls-key", opts.tunnelTLSKey, "server TLS private key file path", "")
			f.StringVar(&opts.tunnelTLSServerName, "tls-server-name", opts.tunnelTLSServerName, "client TLS server name override", "")
			f.BoolVar(&opts.tunnelTLSInsecure, "tls-insecure", opts.tunnelTLSInsecure, "client skips TLS certificate verification", "")
			f.StringVar(&opts.tunnelSecurity, "tunnel-security", opts.tunnelSecurity, "tunnel security: none or reality", "")
			f.StringVar(&opts.tunnelFlow, "flow", opts.tunnelFlow, "VLESS flow, for example xtls-rprx-vision", "")
			f.StringVar(&opts.realityServerName, "reality-server-name", opts.realityServerName, "REALITY client serverName", "")
			f.StringVar(&opts.realityServerNames, "reality-server-names", opts.realityServerNames, "comma-separated REALITY serverNames for server config", "")
			f.StringVar(&opts.realityFingerprint, "reality-fingerprint", opts.realityFingerprint, "REALITY uTLS fingerprint", "")
			f.StringVar(&opts.realityPublicKey, "reality-public-key", opts.realityPublicKey, "REALITY client publicKey", "")
			f.StringVar(&opts.realityPrivateKey, "reality-private-key", opts.realityPrivateKey, "REALITY server privateKey", "")
			f.StringVar(&opts.realityShortID, "reality-short-id", opts.realityShortID, "REALITY client shortId hex", "")
			f.StringVar(&opts.realityShortIDs, "reality-short-ids", opts.realityShortIDs, "comma-separated REALITY server shortIds in hex", "")
			f.StringVar(&opts.realityDest, "reality-dest", opts.realityDest, "REALITY fallback destination host:port", "")
			f.StringVar(&opts.realitySpiderX, "reality-spider-x", opts.realitySpiderX, "REALITY spiderX path", "")
			f.StringVar(&opts.tunnelMux, "mux", opts.tunnelMux, "tunnel mux setting: true or false; empty keeps default", "")
			f.StringVar(&opts.upstreamProtocol, "client-upstream-protocol", opts.upstreamProtocol, "client upstream protocol: socks5 or mixed", "")
			f.StringVar(&opts.socks5Username, "client-socks5-username", opts.socks5Username, "local SOCKS5 username written to client config", "")
			f.StringVar(&opts.socks5Password, "client-socks5-password", opts.socks5Password, "local SOCKS5 password written to client config", "")
			f.StringVar(&opts.upstreamSOCKS5Username, "client-upstream-socks5-username", opts.upstreamSOCKS5Username, "upstream SOCKS5 username written to client config", "")
			f.StringVar(&opts.upstreamSOCKS5Password, "client-upstream-socks5-password", opts.upstreamSOCKS5Password, "upstream SOCKS5 password written to client config", "")
			f.StringVar(&opts.forceCIDRs, "force-ip-cidrs", opts.forceCIDRs, "comma-separated IP CIDRs to force upstream in route config", "")
			f.BoolVar(&opts.overwrite, "overwrite", opts.overwrite, "overwrite existing output files", "")
		},
		Run: func(ctx context.Context, c *cmd.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("unexpected args: %v", args)
			}
			if !hasExplicitConfigGenerateFlags(os.Args[1:]) {
				return runInteractiveConfig(ctx, opts, os.Stdin, os.Stdout, os.Stderr)
			}
			return generateConfigFiles(opts)
		},
	}
}

func generateConfigFiles(opts generateConfigOptions) error {
	return generateConfigFilesWithOutput(opts, os.Stdout)
}

func generateConfigFilesWithOutput(opts generateConfigOptions, out io.Writer) error {
	opts = applyGenerateConfigDefaults(opts)
	normalizedTarget, err := normalizeConfigTarget(opts.target)
	if err != nil {
		return err
	}
	protocol, err := normalizeGeneratedProtocol(opts.protocol)
	if err != nil {
		return err
	}
	transport, err := normalizeGeneratedTransport(opts.transport)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(opts.token)
	if token == "" {
		token, err = generateTokenForProtocol(protocol)
		if err != nil {
			return err
		}
	}
	mux, muxSet, err := parseOptionalBool(opts.tunnelMux)
	if err != nil {
		return err
	}
	forceCIDRs := splitCommaList(opts.forceCIDRs)
	if err := validateGeneratedOptions(normalizedTarget, protocol, opts, token); err != nil {
		return err
	}
	if err := prepareGeneratedRealityKeys(normalizedTarget, &opts); err != nil {
		return err
	}
	serverCfg, clientCfg := buildGeneratedConfigs(protocol, transport, token, opts, mux, muxSet)
	routeCfg := buildGeneratedRouteConfig(forceCIDRs)
	writes := make([]configWrite, 0, 3)
	switch normalizedTarget {
	case configTargetBoth:
		writes = append(writes,
			configWrite{path: resolveGeneratedOutput(opts.outDir, opts.serverOutput), cfg: serverCfg},
			configWrite{path: resolveGeneratedOutput(opts.outDir, opts.clientOutput), cfg: clientCfg},
			configWrite{path: resolveGeneratedOutput(opts.outDir, opts.routeOutput), cfg: routeCfg},
		)
	case configTargetServer:
		writes = append(writes, configWrite{path: resolveGeneratedOutput(opts.outDir, opts.serverOutput), cfg: serverCfg})
	case configTargetClient:
		output := opts.clientOutput
		if strings.TrimSpace(opts.serverOutput) != "" && opts.serverOutput != "server.json" {
			output = opts.serverOutput
		}
		writes = append(writes,
			configWrite{path: resolveGeneratedOutput(opts.outDir, output), cfg: clientCfg},
			configWrite{path: resolveGeneratedOutput(opts.outDir, opts.routeOutput), cfg: routeCfg},
		)
	default:
		return fmt.Errorf("unsupported config target %q", normalizedTarget)
	}
	for _, write := range writes {
		if err := writeGeneratedConfig(write.path, write.cfg, opts.overwrite); err != nil {
			return err
		}
		if out != nil {
			if _, err := fmt.Fprintf(out, "wrote %s\n", write.path); err != nil {
				return err
			}
		}
	}
	return nil
}

func applyGenerateConfigDefaults(opts generateConfigOptions) generateConfigOptions {
	if strings.TrimSpace(opts.outDir) == "" {
		opts.outDir = "."
	}
	if strings.TrimSpace(opts.serverOutput) == "" {
		opts.serverOutput = "server.json"
	}
	if strings.TrimSpace(opts.clientOutput) == "" {
		opts.clientOutput = "client.json"
	}
	if strings.TrimSpace(opts.routeOutput) == "" {
		opts.routeOutput = "route.json"
	}
	return opts
}

var configGenerateFlagNames = map[string]struct{}{
	"target":                          {},
	"protocol":                        {},
	"transport":                       {},
	"token":                           {},
	"out-dir":                         {},
	"server-output":                   {},
	"client-output":                   {},
	"route-output":                    {},
	"output":                          {},
	"o":                               {},
	"server-listen":                   {},
	"client-listen":                   {},
	"server-addr":                     {},
	"tunnel-path":                     {},
	"tls":                             {},
	"tls-cert":                        {},
	"tls-key":                         {},
	"tls-server-name":                 {},
	"tls-insecure":                    {},
	"tunnel-security":                 {},
	"flow":                            {},
	"reality-server-name":             {},
	"reality-server-names":            {},
	"reality-fingerprint":             {},
	"reality-public-key":              {},
	"reality-private-key":             {},
	"reality-short-id":                {},
	"reality-short-ids":               {},
	"reality-dest":                    {},
	"reality-spider-x":                {},
	"mux":                             {},
	"client-upstream-protocol":        {},
	"client-socks5-username":          {},
	"client-socks5-password":          {},
	"client-upstream-socks5-username": {},
	"client-upstream-socks5-password": {},
	"force-ip-cidrs":                  {},
	"overwrite":                       {},
}

func hasExplicitConfigGenerateFlags(args []string) bool {
	inConfigCommand := false
	for _, arg := range args {
		if !inConfigCommand {
			if isConfigCommandName(arg) {
				inConfigCommand = true
			}
			continue
		}
		if arg == "--" {
			return false
		}
		name, ok := configFlagName(arg)
		if !ok {
			continue
		}
		if _, exists := configGenerateFlagNames[name]; exists {
			return true
		}
	}
	return false
}

func isConfigCommandName(value string) bool {
	switch value {
	case "config", "cfg", "gen":
		return true
	default:
		return false
	}
}

func configFlagName(value string) (string, bool) {
	if strings.HasPrefix(value, "--") {
		name := strings.TrimPrefix(value, "--")
		if name == "" {
			return "", false
		}
		if index := strings.IndexByte(name, '='); index >= 0 {
			name = name[:index]
		}
		return name, name != ""
	}
	if strings.HasPrefix(value, "-") {
		name := strings.TrimPrefix(value, "-")
		if name == "" {
			return "", false
		}
		if index := strings.IndexByte(name, '='); index >= 0 {
			name = name[:index]
		}
		if len(name) > 1 {
			name = name[:1]
		}
		return name, name != ""
	}
	return "", false
}

type configWrite struct {
	path string
	cfg  any
}

type configWizardDriver struct {
	opts generateConfigOptions
}

func runInteractiveConfig(ctx context.Context, opts generateConfigOptions, in io.Reader, out io.Writer, errOut io.Writer) error {
	app := cmd.NewApp("tcptun config")
	app.EnableREPL()
	return app.RunWith(ctx, cmd.REPLRuntime{
		In:      in,
		Out:     out,
		Err:     errOut,
		Welcome: "Interactive config generator. Press Enter to keep the shown default.",
		Driver:  configWizardDriver{opts: opts},
	})
}

func (d configWizardDriver) Run(ctx context.Context, repl *cmd.REPL) error {
	if repl == nil {
		return errors.New("config wizard repl is nil")
	}
	wizard := configWizard{
		reader: bufio.NewReader(repl.In),
		out:    repl.Out,
		opts:   d.opts,
	}
	opts, err := wizard.collect(ctx)
	if err != nil {
		return err
	}
	return generateConfigFilesWithOutput(opts, repl.Out)
}

type configWizard struct {
	reader *bufio.Reader
	out    io.Writer
	opts   generateConfigOptions
}

func (w *configWizard) collect(ctx context.Context) (generateConfigOptions, error) {
	if w == nil || w.reader == nil {
		return generateConfigOptions{}, errors.New("config wizard is not initialized")
	}
	if err := ctx.Err(); err != nil {
		return generateConfigOptions{}, err
	}
	opts := w.opts
	var err error
	opts.protocol, err = w.readChoice("Protocol", []string{
		tcptun.TunnelProtocolNative,
		tcptun.TunnelProtocolVLESS,
		tcptun.TunnelProtocolVMess,
		tcptun.TunnelProtocolTrojan,
	}, opts.protocol, normalizeGeneratedProtocol)
	if err != nil {
		return generateConfigOptions{}, err
	}
	opts.target, err = w.readChoice("Config target", []string{configTargetBoth, configTargetServer, configTargetClient}, opts.target, normalizeConfigTarget)
	if err != nil {
		return generateConfigOptions{}, err
	}
	opts.transport, err = w.readChoice("Tunnel transport", []string{
		tcptun.TunnelTransportRaw,
		tcptun.TunnelTransportWS,
		tcptun.TunnelTransportH2,
		tcptun.TunnelTransportH3,
	}, opts.transport, normalizeGeneratedTransport)
	if err != nil {
		return generateConfigOptions{}, err
	}
	opts.serverAddr, err = w.readString("Server address for client config", opts.serverAddr)
	if err != nil {
		return generateConfigOptions{}, err
	}
	opts.serverListen, err = w.readString("Server listen address", opts.serverListen)
	if err != nil {
		return generateConfigOptions{}, err
	}
	opts.clientListen, err = w.readString("Client listen address", opts.clientListen)
	if err != nil {
		return generateConfigOptions{}, err
	}
	opts.tunnelPath, err = w.readString("Tunnel path", opts.tunnelPath)
	if err != nil {
		return generateConfigOptions{}, err
	}
	opts.token, err = w.readString("Shared token/password/UUID (empty = auto generate)", opts.token)
	if err != nil {
		return generateConfigOptions{}, err
	}
	opts.tunnelSecurity = "none"
	if opts.protocol == tcptun.TunnelProtocolVLESS {
		opts.tunnelSecurity, err = w.readChoice("Tunnel security", []string{"none", "reality"}, opts.tunnelSecurity, normalizeGeneratedSecurity)
		if err != nil {
			return generateConfigOptions{}, err
		}
	}
	if opts.tunnelSecurity == "reality" {
		opts.tunnelFlow, err = w.readString("VLESS flow", defaultString(opts.tunnelFlow, "xtls-rprx-vision"))
		if err != nil {
			return generateConfigOptions{}, err
		}
		opts.realityServerName, err = w.readString("REALITY client serverName", opts.realityServerName)
		if err != nil {
			return generateConfigOptions{}, err
		}
		opts.realityServerNames, err = w.readString("REALITY serverNames", opts.realityServerNames)
		if err != nil {
			return generateConfigOptions{}, err
		}
		opts.realityFingerprint, err = w.readString("REALITY fingerprint", defaultString(opts.realityFingerprint, "chrome"))
		if err != nil {
			return generateConfigOptions{}, err
		}
		if configTargetIncludesServer(opts.target) {
			opts.realityPrivateKey, err = w.readString("REALITY server privateKey (empty = auto generate)", opts.realityPrivateKey)
			if err != nil {
				return generateConfigOptions{}, err
			}
		}
		if configTargetIncludesClient(opts.target) && !configTargetIncludesServer(opts.target) {
			opts.realityPublicKey, err = w.readString("REALITY client publicKey", opts.realityPublicKey)
			if err != nil {
				return generateConfigOptions{}, err
			}
		}
		opts.realityShortID, err = w.readString("REALITY client shortId", opts.realityShortID)
		if err != nil {
			return generateConfigOptions{}, err
		}
		opts.realityShortIDs, err = w.readString("REALITY server shortIds", opts.realityShortIDs)
		if err != nil {
			return generateConfigOptions{}, err
		}
		opts.realityDest, err = w.readString("REALITY fallback dest", opts.realityDest)
		if err != nil {
			return generateConfigOptions{}, err
		}
		opts.realitySpiderX, err = w.readString("REALITY spiderX", defaultString(opts.realitySpiderX, "/"))
		if err != nil {
			return generateConfigOptions{}, err
		}
	} else {
		opts.tunnelTLS, err = w.readBool("Enable TLS", opts.tunnelTLS)
		if err != nil {
			return generateConfigOptions{}, err
		}
		if opts.tunnelTLS {
			opts.tunnelTLSCert, err = w.readString("Server TLS certificate path", opts.tunnelTLSCert)
			if err != nil {
				return generateConfigOptions{}, err
			}
			opts.tunnelTLSKey, err = w.readString("Server TLS private key path", opts.tunnelTLSKey)
			if err != nil {
				return generateConfigOptions{}, err
			}
			opts.tunnelTLSServerName, err = w.readString("Client TLS server name", opts.tunnelTLSServerName)
			if err != nil {
				return generateConfigOptions{}, err
			}
			opts.tunnelTLSInsecure, err = w.readBool("Client skip TLS verification", opts.tunnelTLSInsecure)
			if err != nil {
				return generateConfigOptions{}, err
			}
		}
	}
	opts.tunnelMux, err = w.readString("Tunnel mux true/false", defaultString(opts.tunnelMux, "true"))
	if err != nil {
		return generateConfigOptions{}, err
	}
	opts.upstreamProtocol, err = w.readChoice("Client upstream protocol", []string{"socks5", "mixed"}, defaultString(opts.upstreamProtocol, "socks5"), normalizeGeneratedUpstreamProtocol)
	if err != nil {
		return generateConfigOptions{}, err
	}
	opts.socks5Username, err = w.readString("Client local SOCKS5 username", opts.socks5Username)
	if err != nil {
		return generateConfigOptions{}, err
	}
	opts.socks5Password, err = w.readString("Client local SOCKS5 password", opts.socks5Password)
	if err != nil {
		return generateConfigOptions{}, err
	}
	opts.upstreamSOCKS5Username, err = w.readString("Client upstream SOCKS5 username", opts.upstreamSOCKS5Username)
	if err != nil {
		return generateConfigOptions{}, err
	}
	opts.upstreamSOCKS5Password, err = w.readString("Client upstream SOCKS5 password", opts.upstreamSOCKS5Password)
	if err != nil {
		return generateConfigOptions{}, err
	}
	opts.forceCIDRs, err = w.readString("Client force-upstream IP CIDRs", opts.forceCIDRs)
	if err != nil {
		return generateConfigOptions{}, err
	}
	opts.outDir, err = w.readString("Output directory", opts.outDir)
	if err != nil {
		return generateConfigOptions{}, err
	}
	opts.serverOutput, err = w.readString("Server config output", opts.serverOutput)
	if err != nil {
		return generateConfigOptions{}, err
	}
	opts.clientOutput, err = w.readString("Client config output", opts.clientOutput)
	if err != nil {
		return generateConfigOptions{}, err
	}
	opts.routeOutput, err = w.readString("Route config output", opts.routeOutput)
	if err != nil {
		return generateConfigOptions{}, err
	}
	opts.overwrite, err = w.readBool("Overwrite existing files", opts.overwrite)
	if err != nil {
		return generateConfigOptions{}, err
	}
	return opts, nil
}

func (w *configWizard) readChoice(label string, choices []string, fallback string, normalize func(string) (string, error)) (string, error) {
	if len(choices) == 0 {
		return "", errors.New("choice list is empty")
	}
	defaultValue, err := normalize(fallback)
	if err != nil {
		return "", err
	}
	for {
		if _, err := fmt.Fprintf(w.out, "%s:\n", label); err != nil {
			return "", err
		}
		for i, choice := range choices {
			if _, err := fmt.Fprintf(w.out, "  %d) %s\n", i+1, choice); err != nil {
				return "", err
			}
		}
		answer, err := w.readString("Choose", defaultValue)
		if err != nil {
			return "", err
		}
		if index, ok := parseChoiceIndex(answer, len(choices)); ok {
			return choices[index], nil
		}
		value, err := normalize(answer)
		if err == nil {
			return value, nil
		}
		if _, writeErr := fmt.Fprintf(w.out, "Invalid value: %v\n", err); writeErr != nil {
			return "", writeErr
		}
	}
}

func (w *configWizard) readString(label string, fallback string) (string, error) {
	if _, err := fmt.Fprintf(w.out, "%s [%s]: ", label, fallback); err != nil {
		return "", err
	}
	line, err := w.reader.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && line != "" {
			return promptValueOrDefault(line, fallback), nil
		}
		return "", err
	}
	return promptValueOrDefault(line, fallback), nil
}

func (w *configWizard) readBool(label string, fallback bool) (bool, error) {
	for {
		value, err := w.readString(label, strconvBool(fallback))
		if err != nil {
			return false, err
		}
		parsed, _, err := parseOptionalBool(value)
		if err == nil {
			return parsed, nil
		}
		if _, writeErr := fmt.Fprintf(w.out, "Invalid boolean value: %v\n", err); writeErr != nil {
			return false, writeErr
		}
	}
}

func promptValueOrDefault(value string, fallback string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fallback
	}
	return trimmed
}

func parseChoiceIndex(value string, size int) (int, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, false
	}
	index, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, false
	}
	index--
	if index < 0 || index >= size {
		return 0, false
	}
	return index, true
}

func normalizeGeneratedSecurity(value string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	switch trimmed {
	case "", "none":
		return "none", nil
	case "reality":
		return "reality", nil
	default:
		return "", fmt.Errorf("invalid tunnel security %q; supported values: none, reality", value)
	}
}

func normalizeGeneratedUpstreamProtocol(value string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	switch trimmed {
	case "", "socks5":
		return "socks5", nil
	case "mixed":
		return "mixed", nil
	default:
		return "", fmt.Errorf("invalid upstream protocol %q; supported values: socks5, mixed", value)
	}
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func strconvBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func buildGeneratedConfigs(protocol string, transport string, token string, opts generateConfigOptions, mux bool, muxSet bool) (generatedRouteConfig, generatedRouteConfig) {
	serverListenAddrs := splitCommaList(opts.serverListen)
	serverCfg := generatedRouteConfig{
		Mode:            tcptun.ProxyModeServer,
		Token:           token,
		TunnelProtocol:  protocol,
		TunnelTransport: transport,
		TunnelPath:      normalizeGeneratedPath(opts.tunnelPath),
	}
	if len(serverListenAddrs) > 1 {
		serverCfg.ListenAddrs = serverListenAddrs
	} else {
		serverCfg.ListenAddr = strings.TrimSpace(opts.serverListen)
	}
	clientCfg := generatedRouteConfig{
		Mode:                   tcptun.ProxyModeClient,
		ListenAddr:             strings.TrimSpace(opts.clientListen),
		ServerAddr:             strings.TrimSpace(opts.serverAddr),
		Token:                  token,
		TunnelProtocol:         protocol,
		TunnelTransport:        transport,
		TunnelPath:             normalizeGeneratedPath(opts.tunnelPath),
		UpstreamProtocol:       strings.TrimSpace(opts.upstreamProtocol),
		SOCKS5Username:         strings.TrimSpace(opts.socks5Username),
		SOCKS5Password:         opts.socks5Password,
		UpstreamSOCKS5Username: strings.TrimSpace(opts.upstreamSOCKS5Username),
		UpstreamSOCKS5Password: opts.upstreamSOCKS5Password,
	}
	if muxSet {
		serverCfg.TunnelMux = &mux
		clientCfg.TunnelMux = &mux
	}
	serverCfg.ServerAddr = ""
	serverCfg.TunnelTLSCert = strings.TrimSpace(opts.tunnelTLSCert)
	serverCfg.TunnelTLSKey = strings.TrimSpace(opts.tunnelTLSKey)
	clientCfg.TunnelTLS = opts.tunnelTLS
	clientCfg.TunnelTLSServerName = strings.TrimSpace(opts.tunnelTLSServerName)
	clientCfg.TunnelTLSInsecure = opts.tunnelTLSInsecure
	applyGeneratedSecurity(&serverCfg, &clientCfg, opts)
	return serverCfg, clientCfg
}

func buildGeneratedRouteConfig(forceCIDRs []string) generatedRouteRulesConfig {
	cfg := generatedRouteRulesConfig{ForceUpstream: emptyGeneratedForceUpstreamConfig()}
	cfg.ForceUpstream.IPCIDRs = forceCIDRs
	return cfg
}

func emptyGeneratedForceUpstreamConfig() generatedForceUpstreamConfig {
	return generatedForceUpstreamConfig{
		Domains:        []string{},
		DomainRegexes:  []string{},
		DomainSuffixes: []string{},
		IPCIDRs:        []string{},
		IPRanges:       []string{},
		IPs:            []string{},
	}
}

func applyGeneratedSecurity(serverCfg *generatedRouteConfig, clientCfg *generatedRouteConfig, opts generateConfigOptions) {
	security := strings.TrimSpace(opts.tunnelSecurity)
	if security == "" || security == "none" {
		return
	}
	serverCfg.TunnelSecurity = security
	clientCfg.TunnelSecurity = security
	serverCfg.TunnelFlow = strings.TrimSpace(opts.tunnelFlow)
	clientCfg.TunnelFlow = strings.TrimSpace(opts.tunnelFlow)
	serverCfg.RealityPrivateKey = strings.TrimSpace(opts.realityPrivateKey)
	serverCfg.RealityServerNames = splitCommaList(opts.realityServerNames)
	serverCfg.RealityShortIDs = splitCommaList(opts.realityShortIDs)
	serverCfg.RealityDest = strings.TrimSpace(opts.realityDest)
	clientCfg.RealityServerName = strings.TrimSpace(opts.realityServerName)
	clientCfg.RealityFingerprint = strings.TrimSpace(opts.realityFingerprint)
	clientCfg.RealityPublicKey = strings.TrimSpace(opts.realityPublicKey)
	clientCfg.RealityShortID = strings.TrimSpace(opts.realityShortID)
	clientCfg.RealitySpiderX = strings.TrimSpace(opts.realitySpiderX)
}

func prepareGeneratedRealityKeys(target string, opts *generateConfigOptions) error {
	if opts == nil {
		return errors.New("generate config options are nil")
	}
	security := strings.TrimSpace(opts.tunnelSecurity)
	if security == "" || security == "none" {
		return nil
	}
	if security != "reality" {
		return fmt.Errorf("invalid tunnel security %q; supported values: none, reality", security)
	}

	privateKey := strings.TrimSpace(opts.realityPrivateKey)
	publicKey := strings.TrimSpace(opts.realityPublicKey)
	if configTargetIncludesServer(target) && privateKey == "" {
		generated, err := generateRealityPrivateKey()
		if err != nil {
			return err
		}
		privateKey = generated
		opts.realityPrivateKey = generated
	}
	if privateKey != "" {
		derived, err := deriveRealityPublicKey(privateKey)
		if err != nil {
			return err
		}
		if publicKey != "" && publicKey != derived {
			return errors.New("--reality-public-key does not match --reality-private-key")
		}
		publicKey = derived
		if configTargetIncludesClient(target) {
			opts.realityPublicKey = derived
		}
	}
	if configTargetIncludesClient(target) && publicKey == "" {
		return errors.New("--reality-public-key is required when generating REALITY client config unless --reality-private-key is provided")
	}
	return nil
}

func configTargetIncludesServer(target string) bool {
	return target == configTargetBoth || target == configTargetServer
}

func configTargetIncludesClient(target string) bool {
	return target == configTargetBoth || target == configTargetClient
}

func generateRealityPrivateKey() (string, error) {
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate REALITY private key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(privateKey.Bytes()), nil
}

func deriveRealityPublicKey(privateKeyText string) (string, error) {
	privateKeyBytes, err := decodeRealityKey(privateKeyText, "REALITY private key")
	if err != nil {
		return "", err
	}
	privateKey, err := ecdh.X25519().NewPrivateKey(privateKeyBytes)
	if err != nil {
		return "", fmt.Errorf("parse REALITY private key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes()), nil
}

func decodeRealityKey(value string, name string) ([]byte, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, fmt.Errorf("%s is required", name)
	}
	encodings := []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	}
	var decodeErr error
	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(trimmed)
		if err == nil {
			if len(decoded) != 32 {
				return nil, fmt.Errorf("invalid %s length: %d", name, len(decoded))
			}
			return decoded, nil
		}
		decodeErr = err
	}
	return nil, fmt.Errorf("decode %s: %w", name, decodeErr)
}

func validateGeneratedOptions(target string, protocol string, opts generateConfigOptions, token string) error {
	if target == configTargetClient || target == configTargetBoth {
		if strings.TrimSpace(opts.serverAddr) == "" {
			return errors.New("--server-addr is required when generating client config")
		}
	}
	if protocol == tcptun.TunnelProtocolVLESS || protocol == tcptun.TunnelProtocolVMess {
		if _, err := parseGeneratedUUID(token); err != nil {
			return fmt.Errorf("--token must be a UUID for %s: %w", protocol, err)
		}
	}
	security := strings.TrimSpace(opts.tunnelSecurity)
	if security != "" && security != "none" && security != "reality" {
		return fmt.Errorf("invalid tunnel security %q; supported values: none, reality", security)
	}
	if security == "reality" {
		if protocol != tcptun.TunnelProtocolVLESS {
			return errors.New("REALITY config generation requires --protocol vless")
		}
		if opts.tunnelTLS {
			return errors.New("REALITY cannot be combined with --tls")
		}
	}
	if err := validateGeneratedSOCKS5Credentials("socks5", opts.socks5Username, opts.socks5Password); err != nil {
		return err
	}
	if err := validateGeneratedSOCKS5Credentials("upstream socks5", opts.upstreamSOCKS5Username, opts.upstreamSOCKS5Password); err != nil {
		return err
	}
	return nil
}

func validateGeneratedSOCKS5Credentials(name string, username string, password string) error {
	if len(username) > 255 {
		return fmt.Errorf("%s username is too long: %d bytes", name, len(username))
	}
	if len(password) > 255 {
		return fmt.Errorf("%s password is too long: %d bytes", name, len(password))
	}
	return nil
}

func writeGeneratedConfig(path string, cfg any, overwrite bool) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("output path is required")
	}
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists; use --overwrite to replace it", path)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

func resolveGeneratedOutput(outDir string, output string) string {
	trimmed := strings.TrimSpace(output)
	if filepath.IsAbs(trimmed) {
		return trimmed
	}
	base := strings.TrimSpace(outDir)
	if base == "" {
		base = "."
	}
	return filepath.Join(base, trimmed)
}

func normalizeConfigTarget(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", configTargetBoth:
		return configTargetBoth, nil
	case configTargetServer, "s", "srv":
		return configTargetServer, nil
	case configTargetClient, "c", "cli":
		return configTargetClient, nil
	default:
		return "", fmt.Errorf("invalid config target %q; supported values: both, server, client", value)
	}
}

func normalizeGeneratedProtocol(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", tcptun.TunnelProtocolNative:
		return tcptun.TunnelProtocolNative, nil
	case tcptun.TunnelProtocolVLESS:
		return tcptun.TunnelProtocolVLESS, nil
	case tcptun.TunnelProtocolVMess:
		return tcptun.TunnelProtocolVMess, nil
	case tcptun.TunnelProtocolTrojan:
		return tcptun.TunnelProtocolTrojan, nil
	default:
		return "", fmt.Errorf("invalid protocol %q; supported values: native, vless, vmess, trojan", value)
	}
}

func normalizeGeneratedTransport(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", tcptun.TunnelTransportRaw:
		return tcptun.TunnelTransportRaw, nil
	case tcptun.TunnelTransportWS:
		return tcptun.TunnelTransportWS, nil
	case tcptun.TunnelTransportH2:
		return tcptun.TunnelTransportH2, nil
	case tcptun.TunnelTransportH3:
		return tcptun.TunnelTransportH3, nil
	default:
		return "", fmt.Errorf("invalid transport %q; supported values: raw, ws, h2, h3", value)
	}
}

func normalizeGeneratedPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "/") {
		return trimmed
	}
	return "/" + trimmed
}

func parseOptionalBool(value string) (bool, bool, error) {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	switch trimmed {
	case "":
		return false, false, nil
	case "true", "1", "yes", "on":
		return true, true, nil
	case "false", "0", "no", "off":
		return false, true, nil
	default:
		return false, false, fmt.Errorf("invalid bool value %q", value)
	}
}

func generateTokenForProtocol(protocol string) (string, error) {
	switch protocol {
	case tcptun.TunnelProtocolVLESS, tcptun.TunnelProtocolVMess:
		return generateUUIDv4()
	case tcptun.TunnelProtocolNative, tcptun.TunnelProtocolTrojan:
		return generateHexToken(32)
	default:
		return "", fmt.Errorf("unsupported protocol %q", protocol)
	}
}

func generateHexToken(size int) (string, error) {
	if size <= 0 {
		return "", errors.New("token size must be positive")
	}
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func generateUUIDv4() (string, error) {
	uuid := [16]byte{}
	if _, err := rand.Read(uuid[:]); err != nil {
		return "", err
	}
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	uuid[8] = (uuid[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16]), nil
}

func parseGeneratedUUID(value string) ([16]byte, error) {
	normalized := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "")
	if len(normalized) != 32 {
		return [16]byte{}, errors.New("invalid UUID length")
	}
	decoded, err := hex.DecodeString(normalized)
	if err != nil {
		return [16]byte{}, err
	}
	if len(decoded) != 16 {
		return [16]byte{}, errors.New("decoded UUID has invalid length")
	}
	uuid := [16]byte{}
	copy(uuid[:], decoded)
	return uuid, nil
}
