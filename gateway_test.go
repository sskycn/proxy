package main

import (
	"net"
	"testing"
)

func TestParseLinuxDefaultGateway(t *testing.T) {
	input := "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\n" +
		"en0\t00000000\t0101A8C0\t0003\t0\t0\t0\t00000000\n"

	ip, err := parseLinuxDefaultGateway(input)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ip.String(), "192.168.1.1"; got != want {
		t.Fatalf("gateway = %s, want %s", got, want)
	}
}

func TestParseRouteGetDefault(t *testing.T) {
	input := `   route to: default
destination: default
       mask: default
    gateway: 192.168.31.1
 interface: en0`

	ip, err := parseRouteGetDefault(input)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ip.String(), "192.168.31.1"; got != want {
		t.Fatalf("gateway = %s, want %s", got, want)
	}
}

func TestParseWindowsRoutePrint(t *testing.T) {
	input := `
IPv4 Route Table
Active Routes:
Network Destination        Netmask          Gateway       Interface  Metric
          0.0.0.0          0.0.0.0      192.168.0.1   192.168.0.42     35
`

	ip, err := parseWindowsRoutePrint(input)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ip.String(), "192.168.0.1"; got != want {
		t.Fatalf("gateway = %s, want %s", got, want)
	}
}

func TestParseLittleEndianIPv4HexRejectsBadInput(t *testing.T) {
	if ip, err := parseLittleEndianIPv4Hex("bad"); err == nil || ip != nil {
		t.Fatalf("ip = %v, err = %v; want error", ip, err)
	}
}

func TestParseLinuxDefaultGatewayRequiresGatewayFlag(t *testing.T) {
	input := "Iface\tDestination\tGateway\tFlags\n" +
		"en0\t00000000\t0101A8C0\t0001\n"

	ip, err := parseLinuxDefaultGateway(input)
	if err == nil || ip != nil {
		t.Fatalf("ip = %v, err = %v; want error", ip, err)
	}
}

func TestParseRouteGetDefaultNotFound(t *testing.T) {
	ip, err := parseRouteGetDefault("interface: en0")
	if err == nil || ip != nil {
		t.Fatalf("ip = %v, err = %v; want error", ip, err)
	}
}

func TestNetParseIPSanity(t *testing.T) {
	if net.ParseIP("192.168.1.1") == nil {
		t.Fatal("test environment cannot parse IPv4")
	}
}
