package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	proxypkg "sskycn/proxy"
)

func TestGenerateConfigFilesBoth(t *testing.T) {
	dir := t.TempDir()
	opts := generateConfigOptions{
		target:       configTargetBoth,
		protocol:     proxypkg.TunnelProtocolVLESS,
		transport:    proxypkg.TunnelTransportRaw,
		outDir:       dir,
		serverOutput: "server.json",
		clientOutput: "client.json",
		serverListen: "0.0.0.0:9443",
		clientListen: "127.0.0.1:1080",
		serverAddr:   "proxy.example.com:9443",
		tunnelPath:   "/proxy",
		forceCIDRs:   "127.0.0.1/32,10.0.0.0/8",
		overwrite:    true,
	}
	if err := generateConfigFiles(opts); err != nil {
		t.Fatal(err)
	}

	server := readGeneratedConfigForTest(t, filepath.Join(dir, "server.json"))
	client := readGeneratedConfigForTest(t, filepath.Join(dir, "client.json"))
	if server.Mode != proxypkg.ProxyModeServer {
		t.Fatalf("server mode = %q", server.Mode)
	}
	if client.Mode != proxypkg.ProxyModeClient {
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
	if len(client.ForceUpstream.IPCIDRs) != 2 {
		t.Fatalf("force CIDRs = %v", client.ForceUpstream.IPCIDRs)
	}
}

func TestGenerateConfigFilesClientOutputAlias(t *testing.T) {
	dir := t.TempDir()
	opts := generateConfigOptions{
		target:       configTargetClient,
		protocol:     proxypkg.TunnelProtocolTrojan,
		transport:    proxypkg.TunnelTransportRaw,
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
	if client.Mode != proxypkg.ProxyModeClient {
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

func TestRunInteractiveConfigGeneratesBothConfigs(t *testing.T) {
	dir := t.TempDir()
	opts := generateConfigOptions{
		target:       configTargetBoth,
		protocol:     proxypkg.TunnelProtocolCustom,
		transport:    proxypkg.TunnelTransportRaw,
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
		dir,
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
	if server.TunnelProtocol != proxypkg.TunnelProtocolVLESS {
		t.Fatalf("server protocol = %q", server.TunnelProtocol)
	}
	if client.ServerAddr != "proxy.example.com:9443" {
		t.Fatalf("client server_addr = %q", client.ServerAddr)
	}
	if client.UpstreamProtocol != "socks5" {
		t.Fatalf("client upstream_protocol = %q", client.UpstreamProtocol)
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
