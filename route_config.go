package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type routeConfigFile struct {
	Mode                string              `json:"mode,omitempty"`
	ListenAddr          string              `json:"listen_addr,omitempty"`
	ServerAddr          string              `json:"server_addr,omitempty"`
	Token               string              `json:"token,omitempty"`
	TunnelProtocol      string              `json:"tunnel_protocol,omitempty"`
	TunnelTransport     string              `json:"tunnel_transport,omitempty"`
	TunnelPath          string              `json:"tunnel_path,omitempty"`
	TunnelTLS           bool                `json:"tunnel_tls,omitempty"`
	TunnelTLSCert       string              `json:"tunnel_tls_cert,omitempty"`
	TunnelTLSKey        string              `json:"tunnel_tls_key,omitempty"`
	TunnelTLSServerName string              `json:"tunnel_tls_server_name,omitempty"`
	TunnelTLSInsecure   bool                `json:"tunnel_tls_insecure,omitempty"`
	TunnelSecurity      string              `json:"tunnel_security,omitempty"`
	TunnelFlow          string              `json:"tunnel_flow,omitempty"`
	RealityServerName   string              `json:"reality_server_name,omitempty"`
	RealityServerNames  []string            `json:"reality_server_names,omitempty"`
	RealityFingerprint  string              `json:"reality_fingerprint,omitempty"`
	RealityPublicKey    string              `json:"reality_public_key,omitempty"`
	RealityPrivateKey   string              `json:"reality_private_key,omitempty"`
	RealityShortID      string              `json:"reality_short_id,omitempty"`
	RealityShortIDs     []string            `json:"reality_short_ids,omitempty"`
	RealityDest         string              `json:"reality_dest,omitempty"`
	RealitySpiderX      string              `json:"reality_spider_x,omitempty"`
	TunnelMux           *bool               `json:"tunnel_mux,omitempty"`
	UpstreamProtocol    string              `json:"upstream_protocol,omitempty"`
	ForceUpstream       forceUpstreamConfig `json:"force_upstream"`
}

type forceUpstreamConfig struct {
	Domains        []string `json:"domains"`
	DomainPrefixes []string `json:"domain_prefixes"`
	DomainSuffixes []string `json:"domain_suffixes"`
	IPCIDRs        []string `json:"ip_cidrs"`
	IPRanges       []string `json:"ip_ranges"`
	IPs            []string `json:"ips"`
}

type routeRules struct {
	forceUpstream forceUpstreamRules
}

type forceUpstreamRules struct {
	domains        map[string]struct{}
	domainPrefixes []string
	domainSuffixes []string
	ipPrefixes     []netip.Prefix
}

func loadRouteRules(path string) (*routeRules, error) {
	rules := &routeRules{}
	if strings.TrimSpace(path) == "" {
		return rules, nil
	}
	cfg, err := readRouteConfig(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return rules, nil
		}
		return nil, err
	}

	compiled, err := compileForceUpstreamRules(cfg.ForceUpstream)
	if err != nil {
		return nil, err
	}
	rules.forceUpstream = compiled
	return rules, nil
}

func loadConfiguredUpstreamProtocol(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	cfg, err := readRouteConfig(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return cfg.UpstreamProtocol, nil
}

func applyRuntimeConfigDefaults(cfg *config) error {
	if cfg == nil || strings.TrimSpace(cfg.ConfigPath) == "" {
		return nil
	}
	fileCfg, err := readRouteConfig(cfg.ConfigPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(cfg.Mode) == "" {
		cfg.Mode = fileCfg.Mode
	}
	if strings.TrimSpace(fileCfg.ListenAddr) != "" && (strings.TrimSpace(cfg.ListenAddr) == "" || cfg.ListenAddr == DefaultConfig().ListenAddr) {
		cfg.ListenAddr = fileCfg.ListenAddr
	}
	if strings.TrimSpace(cfg.ServerAddr) == "" {
		cfg.ServerAddr = fileCfg.ServerAddr
	}
	if strings.TrimSpace(cfg.Token) == "" {
		cfg.Token = fileCfg.Token
	}
	if strings.TrimSpace(cfg.TunnelProtocol) == "" {
		cfg.TunnelProtocol = fileCfg.TunnelProtocol
	}
	if strings.TrimSpace(cfg.TunnelTransport) == "" {
		cfg.TunnelTransport = fileCfg.TunnelTransport
	}
	if strings.TrimSpace(cfg.TunnelPath) == "" {
		cfg.TunnelPath = fileCfg.TunnelPath
	}
	if !cfg.TunnelTLS {
		cfg.TunnelTLS = fileCfg.TunnelTLS
	}
	if strings.TrimSpace(cfg.TunnelTLSCert) == "" {
		cfg.TunnelTLSCert = fileCfg.TunnelTLSCert
	}
	if strings.TrimSpace(cfg.TunnelTLSKey) == "" {
		cfg.TunnelTLSKey = fileCfg.TunnelTLSKey
	}
	if strings.TrimSpace(cfg.TunnelTLSServerName) == "" {
		cfg.TunnelTLSServerName = fileCfg.TunnelTLSServerName
	}
	if !cfg.TunnelTLSInsecure {
		cfg.TunnelTLSInsecure = fileCfg.TunnelTLSInsecure
	}
	if strings.TrimSpace(cfg.TunnelSecurity) == "" {
		cfg.TunnelSecurity = fileCfg.TunnelSecurity
	}
	if strings.TrimSpace(cfg.TunnelFlow) == "" {
		cfg.TunnelFlow = fileCfg.TunnelFlow
	}
	if strings.TrimSpace(cfg.RealityServerName) == "" {
		cfg.RealityServerName = fileCfg.RealityServerName
	}
	if len(cfg.RealityServerNames) == 0 {
		cfg.RealityServerNames = fileCfg.RealityServerNames
	}
	if strings.TrimSpace(cfg.RealityFingerprint) == "" {
		cfg.RealityFingerprint = fileCfg.RealityFingerprint
	}
	if strings.TrimSpace(cfg.RealityPublicKey) == "" {
		cfg.RealityPublicKey = fileCfg.RealityPublicKey
	}
	if strings.TrimSpace(cfg.RealityPrivateKey) == "" {
		cfg.RealityPrivateKey = fileCfg.RealityPrivateKey
	}
	if strings.TrimSpace(cfg.RealityShortID) == "" {
		cfg.RealityShortID = fileCfg.RealityShortID
	}
	if len(cfg.RealityShortIDs) == 0 {
		cfg.RealityShortIDs = fileCfg.RealityShortIDs
	}
	if strings.TrimSpace(cfg.RealityDest) == "" {
		cfg.RealityDest = fileCfg.RealityDest
	}
	if strings.TrimSpace(cfg.RealitySpiderX) == "" {
		cfg.RealitySpiderX = fileCfg.RealitySpiderX
	}
	if fileCfg.TunnelMux != nil {
		cfg.TunnelMux = *fileCfg.TunnelMux
	}
	if strings.TrimSpace(cfg.UpstreamProtocol) == "" {
		cfg.UpstreamProtocol = fileCfg.UpstreamProtocol
	}
	return nil
}

func resolveConfigPath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", nil
	}
	if filepath.IsAbs(trimmed) {
		return trimmed, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	executableDir := filepath.Dir(executable)
	return filepath.Join(executableDir, trimmed), nil
}

func readRouteConfig(path string) (routeConfigFile, error) {
	file, err := os.Open(path)
	if err != nil {
		return routeConfigFile{}, err
	}

	var cfg routeConfigFile
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&cfg); err != nil {
		closeErr := file.Close()
		if closeErr != nil {
			return routeConfigFile{}, errors.Join(err, closeErr)
		}
		return routeConfigFile{}, err
	}
	if err := file.Close(); err != nil {
		return routeConfigFile{}, err
	}
	return cfg, nil
}

func persistDirectFailures(path string, hosts []string) error {
	if strings.TrimSpace(path) == "" || len(hosts) == 0 {
		return nil
	}

	cfg, err := readRouteConfig(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		cfg = routeConfigFile{}
	}

	changed, err := mergeDirectFailures(&cfg.ForceUpstream, hosts)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return writeRouteConfig(path, cfg)
}

func mergeDirectFailures(cfg *forceUpstreamConfig, hosts []string) (bool, error) {
	rules, err := compileForceUpstreamRules(*cfg)
	if err != nil {
		return false, err
	}

	changed := false
	for _, host := range hosts {
		normalized := normalizeTargetHost(host)
		if normalized == "" || rules.matches(normalized) {
			continue
		}
		if addr, err := netip.ParseAddr(normalized); err == nil {
			cfg.IPs = appendUniqueNormalized(cfg.IPs, addr.String())
			if addErr := addIPPrefix(&rules, addr.String()); addErr != nil {
				return false, addErr
			}
			changed = true
			continue
		}
		cfg.Domains = appendUniqueNormalized(cfg.Domains, normalized)
		rules.domains[normalized] = struct{}{}
		changed = true
	}
	if changed {
		normalizeForceUpstreamConfig(cfg)
	}
	return changed, nil
}

func writeRouteConfig(path string, cfg routeConfigFile) error {
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func compileForceUpstreamRules(cfg forceUpstreamConfig) (forceUpstreamRules, error) {
	rules := forceUpstreamRules{domains: make(map[string]struct{}, len(cfg.Domains))}
	for _, domain := range cfg.Domains {
		normalized := normalizeDomainRule(domain)
		if normalized == "" {
			continue
		}
		rules.domains[normalized] = struct{}{}
	}
	for _, prefix := range cfg.DomainPrefixes {
		normalized := normalizeDomainRule(prefix)
		if normalized == "" {
			continue
		}
		rules.domainPrefixes = append(rules.domainPrefixes, normalized)
	}
	for _, suffix := range cfg.DomainSuffixes {
		normalized := normalizeDomainRule(suffix)
		if normalized == "" {
			continue
		}
		rules.domainSuffixes = append(rules.domainSuffixes, strings.TrimPrefix(normalized, "."))
	}
	for _, cidr := range append(append([]string{}, cfg.IPCIDRs...), cfg.IPRanges...) {
		if err := addIPPrefix(&rules, cidr); err != nil {
			return forceUpstreamRules{}, err
		}
	}
	for _, ip := range cfg.IPs {
		if err := addIPPrefix(&rules, ip); err != nil {
			return forceUpstreamRules{}, err
		}
	}
	return rules, nil
}

func addIPPrefix(rules *forceUpstreamRules, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if strings.Contains(trimmed, "/") {
		prefix, err := netip.ParsePrefix(trimmed)
		if err != nil {
			return fmt.Errorf("invalid force_upstream IP CIDR %q: %w", value, err)
		}
		rules.ipPrefixes = append(rules.ipPrefixes, prefix)
		return nil
	}
	addr, err := netip.ParseAddr(trimmed)
	if err != nil {
		return fmt.Errorf("invalid force_upstream IP %q: %w", value, err)
	}
	bits := 128
	if addr.Is4() {
		bits = 32
	}
	rules.ipPrefixes = append(rules.ipPrefixes, netip.PrefixFrom(addr, bits))
	return nil
}

func (r *routeRules) shouldForceUpstream(host string) bool {
	if r == nil {
		return false
	}
	return r.forceUpstream.matches(host)
}

func (r forceUpstreamRules) matches(host string) bool {
	normalized := normalizeTargetHost(host)
	if normalized == "" {
		return false
	}
	if addr, err := netip.ParseAddr(normalized); err == nil {
		for _, prefix := range r.ipPrefixes {
			if prefix.Contains(addr) {
				return true
			}
		}
		return false
	}
	if _, ok := r.domains[normalized]; ok {
		return true
	}
	for _, prefix := range r.domainPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	for _, suffix := range r.domainSuffixes {
		if normalized == suffix || strings.HasSuffix(normalized, "."+suffix) {
			return true
		}
	}
	return false
}

func normalizeDomainRule(value string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
}

func normalizeTargetHost(host string) string {
	return normalizeDomainRule(trimHostBrackets(host))
}

func normalizeForceUpstreamConfig(cfg *forceUpstreamConfig) {
	cfg.Domains = normalizeUniqueStrings(cfg.Domains, normalizeDomainRule)
	cfg.DomainPrefixes = normalizeUniqueStrings(cfg.DomainPrefixes, normalizeDomainRule)
	cfg.DomainSuffixes = normalizeUniqueStrings(cfg.DomainSuffixes, func(value string) string {
		return strings.TrimPrefix(normalizeDomainRule(value), ".")
	})
	cfg.IPs = normalizeUniqueStrings(cfg.IPs, normalizeIPRule)
	cfg.IPCIDRs = normalizeUniqueStrings(cfg.IPCIDRs, normalizeCIDRRule)
	cfg.IPRanges = normalizeUniqueStrings(cfg.IPRanges, normalizeCIDRRule)
}

func normalizeUniqueStrings(values []string, normalize func(string) string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized := normalize(value)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	sort.Strings(out)
	return out
}

func appendUniqueNormalized(values []string, value string) []string {
	normalized := normalizeDomainRule(value)
	if normalized == "" {
		return values
	}
	for _, existing := range values {
		if normalizeDomainRule(existing) == normalized {
			return values
		}
	}
	return append(values, normalized)
}

func normalizeIPRule(value string) string {
	addr, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return addr.String()
}

func normalizeCIDRRule(value string) string {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return prefix.String()
}
