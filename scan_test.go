package main

import (
	"net"
	"testing"
)

func TestForEachIPv4HostSkipsNetworkAndBroadcast(t *testing.T) {
	_, network, err := net.ParseCIDR("192.168.1.0/30")
	if err != nil {
		t.Fatal(err)
	}

	var hosts []string
	forEachIPv4Host(network, func(ip net.IP) bool {
		hosts = append(hosts, ip.String())
		return true
	})

	if got, want := len(hosts), 2; got != want {
		t.Fatalf("host count = %d, want %d (%v)", got, want, hosts)
	}
	if hosts[0] != "192.168.1.1" || hosts[1] != "192.168.1.2" {
		t.Fatalf("hosts = %v", hosts)
	}
}

func TestIPv4Uint32RoundTrip(t *testing.T) {
	ip := net.ParseIP("10.20.30.40")
	value := ipv4ToUint32(ip)
	if got, want := uint32ToIPv4(value).String(), "10.20.30.40"; got != want {
		t.Fatalf("round trip = %s, want %s", got, want)
	}
}

func TestIPv4AddrNetwork(t *testing.T) {
	addr := &net.IPNet{
		IP:   net.ParseIP("192.168.31.42"),
		Mask: net.CIDRMask(24, 32),
	}

	ip, network, ok := ipv4AddrNetwork(addr)
	if !ok {
		t.Fatal("expected IPv4 network")
	}
	if got, want := ip.String(), "192.168.31.42"; got != want {
		t.Fatalf("ip = %s, want %s", got, want)
	}
	if got, want := network.String(), "192.168.31.0/24"; got != want {
		t.Fatalf("network = %s, want %s", got, want)
	}
}

func TestIsUsefulIPv4Network(t *testing.T) {
	_, useful, err := net.ParseCIDR("192.168.1.0/24")
	if err != nil {
		t.Fatal(err)
	}
	_, pointToPoint, err := net.ParseCIDR("192.168.1.0/31")
	if err != nil {
		t.Fatal(err)
	}

	if !isUsefulIPv4Network(useful) {
		t.Fatal("/24 should be useful")
	}
	if isUsefulIPv4Network(pointToPoint) {
		t.Fatal("/31 should not be scanned")
	}
}

func TestLocalIPv4NetworksFromInterfacesKeepsOnlyInternalIPv4(t *testing.T) {
	publicAddr := mustIPv4Net(t, "203.0.113.10/24")
	privateAddr := mustIPv4Net(t, "192.168.31.42/24")
	linkLocalAddr := mustIPv4Net(t, "169.254.1.20/24")
	loopbackAddr := mustIPv4Net(t, "127.0.0.1/8")

	networks := localIPv4NetworksFromInterfaces([]localInterface{
		{flags: net.FlagUp, addrs: []net.Addr{publicAddr}},
		{flags: net.FlagUp, addrs: []net.Addr{privateAddr}},
		{flags: net.FlagUp, addrs: []net.Addr{linkLocalAddr}},
		{flags: net.FlagUp | net.FlagLoopback, addrs: []net.Addr{loopbackAddr}},
	}, net.ParseIP("192.168.31.1"))

	if got, want := len(networks), 2; got != want {
		t.Fatalf("network count = %d, want %d (%v)", got, want, networks)
	}
	if got, want := networks[0].local.String(), "192.168.31.42"; got != want {
		t.Fatalf("first local IP = %s, want %s", got, want)
	}
	if got, want := networks[1].local.String(), "169.254.1.20"; got != want {
		t.Fatalf("second local IP = %s, want %s", got, want)
	}
}

func TestLocalIPv4NetworksFromInterfacesRejectsPublicOnly(t *testing.T) {
	networks := localIPv4NetworksFromInterfaces([]localInterface{
		{flags: net.FlagUp, addrs: []net.Addr{mustIPv4Net(t, "203.0.113.10/24")}},
	}, nil)

	if len(networks) != 0 {
		t.Fatalf("networks = %v, want empty", networks)
	}
}

func TestHasInternalIPv4FromInterfacesDoesNotRequireScannableNetwork(t *testing.T) {
	if hasInternalIPv4FromInterfaces([]localInterface{
		{flags: net.FlagUp, addrs: []net.Addr{mustIPv4Net(t, "203.0.113.10/24")}},
	}) {
		t.Fatal("public-only interfaces should not count as internal")
	}

	if !hasInternalIPv4FromInterfaces([]localInterface{
		{flags: net.FlagUp, addrs: []net.Addr{mustIPv4Net(t, "10.0.0.2/32")}},
	}) {
		t.Fatal("internal /32 address should count as internal")
	}
}

func TestIsInternalDiscoveryIPv4(t *testing.T) {
	for _, tc := range []struct {
		ip   string
		want bool
	}{
		{ip: "192.168.1.20", want: true},
		{ip: "10.0.0.2", want: true},
		{ip: "172.16.0.2", want: true},
		{ip: "169.254.1.2", want: true},
		{ip: "203.0.113.10", want: false},
		{ip: "127.0.0.1", want: false},
		{ip: "0.0.0.0", want: false},
	} {
		if got := isInternalDiscoveryIPv4(net.ParseIP(tc.ip)); got != tc.want {
			t.Fatalf("isInternalDiscoveryIPv4(%s) = %v, want %v", tc.ip, got, tc.want)
		}
	}
}

func mustIPv4Net(t *testing.T, cidr string) *net.IPNet {
	t.Helper()
	ip, network, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	network.IP = ip
	return network
}
