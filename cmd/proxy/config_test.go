package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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
