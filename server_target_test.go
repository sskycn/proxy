package tcptun

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
)

func TestIsPublicServerTargetAddr(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want bool
	}{
		{name: "public ipv4", addr: "8.8.8.8", want: true},
		{name: "public ipv6", addr: "2606:4700:4700::1111", want: true},
		{name: "private ipv4", addr: "10.0.0.1", want: false},
		{name: "loopback ipv4", addr: "127.0.0.1", want: false},
		{name: "cgnat ipv4", addr: "100.64.0.1", want: false},
		{name: "test net ipv4", addr: "203.0.113.1", want: false},
		{name: "multicast ipv4", addr: "224.0.0.1", want: false},
		{name: "ula ipv6", addr: "fd00::1", want: false},
		{name: "link local ipv6", addr: "fe80::1", want: false},
		{name: "documentation ipv6", addr: "2001:db8::1", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := netip.MustParseAddr(tt.addr)
			if got := isPublicServerTargetAddr(addr); got != tt.want {
				t.Fatalf("isPublicServerTargetAddr(%s) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

func TestResolvePublicServerTargetIPRejectsPrivateLiteral(t *testing.T) {
	_, err := resolvePublicServerTargetIP(context.Background(), nil, "127.0.0.1")
	if !errors.Is(err, errServerTargetNotPublic) {
		t.Fatalf("resolve private literal error = %v, want %v", err, errServerTargetNotPublic)
	}
}

func TestResolvePublicServerTargetIPUsesPublicDNSAnswer(t *testing.T) {
	lookup := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{
			{IP: net.ParseIP("10.0.0.1")},
			{IP: net.ParseIP("8.8.8.8")},
		}, nil
	}
	ip, err := resolvePublicServerTargetIP(context.Background(), lookup, "example.test")
	if err != nil {
		t.Fatal(err)
	}
	if got := ip.String(); got != "8.8.8.8" {
		t.Fatalf("resolved IP = %s, want 8.8.8.8", got)
	}
}

func TestResolvePublicServerTargetIPRejectsPrivateDNSAnswers(t *testing.T) {
	lookup := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{
			{IP: net.ParseIP("10.0.0.1")},
			{IP: net.ParseIP("127.0.0.1")},
		}, nil
	}
	_, err := resolvePublicServerTargetIP(context.Background(), lookup, "internal.test")
	if !errors.Is(err, errServerTargetNotPublic) {
		t.Fatalf("resolve private DNS answers error = %v, want %v", err, errServerTargetNotPublic)
	}
}
