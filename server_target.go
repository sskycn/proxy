package tcptun

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
)

var errServerTargetNotPublic = errors.New("server target is not a public IP")

type ipAddrLookupFunc func(context.Context, string) ([]net.IPAddr, error)

var nonPublicServerTargetPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("255.255.255.255/32"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("::ffff:0:0/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func (s *proxyServer) publicTCPTarget(ctx context.Context, host string, port uint16) (string, error) {
	ip, err := resolvePublicServerTargetIP(ctx, serverTargetLookup(s.dialer.Resolver), host)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(int(port))), nil
}

func (s *proxyServer) publicUDPTarget(ctx context.Context, host string, port uint16) (*net.UDPAddr, error) {
	ip, err := resolvePublicServerTargetIP(ctx, serverTargetLookup(s.dialer.Resolver), host)
	if err != nil {
		return nil, err
	}
	return &net.UDPAddr{IP: ip, Port: int(port)}, nil
}

func serverTargetLookup(resolver *net.Resolver) ipAddrLookupFunc {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return resolver.LookupIPAddr
}

func resolvePublicServerTargetIP(ctx context.Context, lookup ipAddrLookupFunc, host string) (net.IP, error) {
	host = trimHostBrackets(host)
	if host == "" {
		return nil, fmt.Errorf("%w: empty host", errServerTargetNotPublic)
	}
	if ip, ok := publicServerTargetIPFromText(host); ok {
		return ip, nil
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return nil, fmt.Errorf("%w: %s", errServerTargetNotPublic, host)
	}
	if lookup == nil {
		lookup = serverTargetLookup(nil)
	}
	ips, err := lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, candidate := range ips {
		if ip, ok := publicServerTargetIP(candidate.IP); ok {
			return ip, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", errServerTargetNotPublic, host)
}

func publicServerTargetIPFromText(host string) (net.IP, bool) {
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return nil, false
	}
	addr = addr.Unmap()
	if !isPublicServerTargetAddr(addr) {
		return nil, false
	}
	return net.IP(addr.AsSlice()), true
}

func publicServerTargetIP(ip net.IP) (net.IP, bool) {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return nil, false
	}
	addr = addr.Unmap()
	if !isPublicServerTargetAddr(addr) {
		return nil, false
	}
	return net.IP(addr.AsSlice()), true
}

func isPublicServerTargetAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	for _, prefix := range nonPublicServerTargetPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}
