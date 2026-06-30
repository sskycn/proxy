package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sskycn/tcptun"
)

func TestGenerateConfigFilesBoth(t *testing.T) {
	dir := t.TempDir()
	opts := generateConfigOptions{
		target:                 configTargetBoth,
		protocol:               tcptun.TunnelProtocolVLESS,
		transport:              tcptun.TunnelTransportRaw,
		outDir:                 dir,
		serverOutput:           "server.json",
		clientOutput:           "client.json",
		serverListen:           "0.0.0.0:9443",
		clientListen:           "127.0.0.1:1080",
		serverAddr:             "proxy.example.com:9443",
		tunnelPath:             "/proxy",
		socks5Username:         "local-user",
		socks5Password:         "local-pass",
		upstreamSOCKS5Username: "up-user",
		upstreamSOCKS5Password: "up-pass",
		forceCIDRs:             "127.0.0.1/32,10.0.0.0/8",
		overwrite:              true,
	}
	if err := generateConfigFiles(opts); err != nil {
		t.Fatal(err)
	}

	server := readGeneratedConfigForTest(t, filepath.Join(dir, "server.json"))
	client := readGeneratedConfigForTest(t, filepath.Join(dir, "client.json"))
	route := readGeneratedRouteConfigForTest(t, filepath.Join(dir, "route.json"))
	if server.Mode != tcptun.ProxyModeServer {
		t.Fatalf("server mode = %q", server.Mode)
	}
	if client.Mode != tcptun.ProxyModeClient {
		t.Fatalf("client mode = %q", client.Mode)
	}
	if server.Token == "" || server.Token != client.Token {
		t.Fatalf("token mismatch: server=%q client=%q", server.Token, client.Token)
	}
	if _, err := parseGeneratedUUID(server.Token); err != nil {
		t.Fatalf("generated token is not UUID: %v", err)
	}
	if client.ServerAddr != "proxy.example.com:9443" {
		t.Fatalf("client server_addr = %q", client.ServerAddr)
	}
	if client.SOCKS5Username != "local-user" || client.SOCKS5Password != "local-pass" {
		t.Fatalf("client local socks credentials = %q/%q", client.SOCKS5Username, client.SOCKS5Password)
	}
	if client.UpstreamSOCKS5Username != "up-user" || client.UpstreamSOCKS5Password != "up-pass" {
		t.Fatalf("client upstream socks credentials = %q/%q", client.UpstreamSOCKS5Username, client.UpstreamSOCKS5Password)
	}
	if len(route.ForceUpstream.IPCIDRs) != 2 {
		t.Fatalf("force CIDRs = %v", route.ForceUpstream.IPCIDRs)
	}
	if generatedConfigHasField(t, filepath.Join(dir, "client.json"), "force_upstream") {
		t.Fatal("client.json should not contain force_upstream")
	}
}

func TestGenerateConfigFilesClientOutputAlias(t *testing.T) {
	dir := t.TempDir()
	opts := generateConfigOptions{
		target:       configTargetClient,
		protocol:     tcptun.TunnelProtocolTrojan,
		transport:    tcptun.TunnelTransportRaw,
		token:        "secret",
		outDir:       dir,
		serverOutput: "single.json",
		clientOutput: "client.json",
		serverAddr:   "proxy.example.com:443",
		overwrite:    true,
	}
	if err := generateConfigFiles(opts); err != nil {
		t.Fatal(err)
	}
	client := readGeneratedConfigForTest(t, filepath.Join(dir, "single.json"))
	if client.Mode != tcptun.ProxyModeClient {
		t.Fatalf("mode = %q", client.Mode)
	}
	if client.Token != "secret" {
		t.Fatalf("token = %q", client.Token)
	}
	if _, err := os.Stat(filepath.Join(dir, "client.json")); err == nil {
		t.Fatal("client.json should not be created when --output alias is used")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestGenerateConfigFilesRealityAutoGeneratesKeys(t *testing.T) {
	dir := t.TempDir()
	opts := generateConfigOptions{
		target:             configTargetBoth,
		protocol:           tcptun.TunnelProtocolVLESS,
		transport:          tcptun.TunnelTransportRaw,
		outDir:             dir,
		serverOutput:       "server.json",
		clientOutput:       "client.json",
		serverListen:       "0.0.0.0:443",
		clientListen:       "127.0.0.1:1080",
		serverAddr:         "proxy.example.com:443",
		tunnelSecurity:     "reality",
		tunnelFlow:         "xtls-rprx-vision",
		realityServerName:  "example.com",
		realityServerNames: "example.com",
		realityDest:        "example.com:443",
		realityFingerprint: "chrome",
		realityShortID:     "",
		realityShortIDs:    "",
		realitySpiderX:     "/",
		overwrite:          true,
	}
	if err := generateConfigFiles(opts); err != nil {
		t.Fatal(err)
	}

	server := readGeneratedConfigForTest(t, filepath.Join(dir, "server.json"))
	client := readGeneratedConfigForTest(t, filepath.Join(dir, "client.json"))
	if server.RealityPrivateKey == "" {
		t.Fatal("server reality_private_key is empty")
	}
	wantPublicKey, err := deriveRealityPublicKey(server.RealityPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if client.RealityPublicKey != wantPublicKey {
		t.Fatalf("client reality_public_key = %q, want %q", client.RealityPublicKey, wantPublicKey)
	}
	if server.RealityPublicKey != "" {
		t.Fatalf("server reality_public_key = %q, want empty", server.RealityPublicKey)
	}
	if client.RealityPrivateKey != "" {
		t.Fatalf("client reality_private_key = %q, want empty", client.RealityPrivateKey)
	}
}

func TestRunInteractiveConfigGeneratesBothConfigs(t *testing.T) {
	dir := t.TempDir()
	opts := generateConfigOptions{
		target:       configTargetBoth,
		protocol:     tcptun.TunnelProtocolNative,
		transport:    tcptun.TunnelTransportRaw,
		outDir:       ".",
		serverOutput: "server.json",
		clientOutput: "client.json",
		serverListen: "0.0.0.0:9443",
		clientListen: "127.0.0.1:1080",
		serverAddr:   "127.0.0.1:9443",
		tunnelPath:   "/proxy",
	}
	input := strings.Join([]string{
		"2",
		"",
		"",
		"proxy.example.com:9443",
		"",
		"",
		"",
		"",
		"",
		"",
		"",
		"",
		"",
		"",
		"",
		"",
		"",
		dir,
		"",
		"",
		"",
		"",
	}, "\n") + "\n"
	var output strings.Builder
	if err := runInteractiveConfig(t.Context(), opts, strings.NewReader(input), &output, &output); err != nil {
		t.Fatal(err)
	}

	server := readGeneratedConfigForTest(t, filepath.Join(dir, "server.json"))
	client := readGeneratedConfigForTest(t, filepath.Join(dir, "client.json"))
	if server.TunnelProtocol != tcptun.TunnelProtocolVLESS {
		t.Fatalf("server protocol = %q", server.TunnelProtocol)
	}
	if client.ServerAddr != "proxy.example.com:9443" {
		t.Fatalf("client server_addr = %q", client.ServerAddr)
	}
	if client.UpstreamProtocol != "socks5" {
		t.Fatalf("client upstream_protocol = %q", client.UpstreamProtocol)
	}
	if generatedConfigHasField(t, filepath.Join(dir, "client.json"), "force_upstream") {
		t.Fatal("client.json should not contain force_upstream")
	}
	if server.TunnelMux == nil || !*server.TunnelMux {
		t.Fatalf("server tunnel_mux = %v", server.TunnelMux)
	}
	if _, err := parseGeneratedUUID(server.Token); err != nil {
		t.Fatalf("generated token is not UUID: %v", err)
	}
	if !strings.Contains(output.String(), "Interactive config generator") {
		t.Fatalf("interactive output missing welcome: %q", output.String())
	}
}

func TestRunInteractiveConfigRealityAutoGeneratesKeys(t *testing.T) {
	dir := t.TempDir()
	opts := generateConfigOptions{
		target:       configTargetBoth,
		protocol:     tcptun.TunnelProtocolNative,
		transport:    tcptun.TunnelTransportRaw,
		outDir:       ".",
		serverOutput: "server.json",
		clientOutput: "client.json",
		routeOutput:  "route.json",
		serverListen: "0.0.0.0:9443",
		clientListen: "127.0.0.1:1080",
		serverAddr:   "127.0.0.1:9443",
		tunnelPath:   "/proxy",
	}
	input := strings.Join([]string{
		"2",
		"",
		"",
		"proxy.example.com:443",
		"0.0.0.0:443",
		"",
		"",
		"",
		"2",
		"",
		"example.com",
		"example.com",
		"",
		"",
		"",
		"",
		"example.com:443",
		"",
		"",
		"",
		"",
		"",
		"",
		"",
		"",
		dir,
		"",
		"",
		"",
		"",
	}, "\n") + "\n"
	var output strings.Builder
	if err := runInteractiveConfig(t.Context(), opts, strings.NewReader(input), &output, &output); err != nil {
		t.Fatal(err)
	}

	server := readGeneratedConfigForTest(t, filepath.Join(dir, "server.json"))
	client := readGeneratedConfigForTest(t, filepath.Join(dir, "client.json"))
	if server.RealityPrivateKey == "" {
		t.Fatal("server reality_private_key is empty")
	}
	wantPublicKey, err := deriveRealityPublicKey(server.RealityPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if client.RealityPublicKey != wantPublicKey {
		t.Fatalf("client reality_public_key = %q, want %q", client.RealityPublicKey, wantPublicKey)
	}
}

func TestHasExplicitConfigGenerateFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{name: "no flags", args: []string{"config"}, want: false},
		{name: "root flag before command", args: []string{"--verbose", "config"}, want: false},
		{name: "long flag", args: []string{"config", "--protocol", "vless"}, want: true},
		{name: "long flag with value", args: []string{"cfg", "--protocol=vless"}, want: true},
		{name: "short output flag", args: []string{"gen", "-o", "client.json"}, want: true},
		{name: "root flag after command", args: []string{"config", "--verbose"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hasExplicitConfigGenerateFlags(tc.args)
			if got != tc.want {
				t.Fatalf("hasExplicitConfigGenerateFlags(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func readGeneratedConfigForTest(t *testing.T, path string) generatedRouteConfig {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg generatedRouteConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func readGeneratedRouteConfigForTest(t *testing.T, path string) generatedRouteRulesConfig {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg generatedRouteRulesConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func generatedConfigHasField(t *testing.T, path string, field string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	_, ok := raw[field]
	return ok
}
