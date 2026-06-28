package main

import (
	"context"
	"errors"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"
)

var errReachableProxyNotFound = errors.New("reachable proxy not found")

type ipv4Network struct {
	local   net.IP
	network *net.IPNet
}

func canConnect(ctx context.Context, ip net.IP, port int, timeout time.Duration) bool {
	if ip == nil {
		return false
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(port)))
	if err != nil {
		return false
	}
	if err := conn.Close(); err != nil {
		return false
	}
	return true
}

func scanLocalIPv4(ctx context.Context, port int, timeout time.Duration, workers int, gatewayHint net.IP) (net.IP, error) {
	networks, err := localIPv4Networks(gatewayHint)
	if err != nil {
		return nil, err
	}
	if len(networks) == 0 {
		return nil, errReachableProxyNotFound
	}
	if workers < 1 {
		workers = 1
	}

	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan net.IP, workers*2)
	found := make(chan net.IP, 1)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ip := range jobs {
				if canConnect(scanCtx, ip, port, timeout) {
					select {
					case found <- ip:
						cancel()
					default:
					}
					return
				}
			}
		}()
	}

	feedDone := make(chan struct{})
	go func() {
		defer close(feedDone)
		defer close(jobs)

		seen := make(map[uint32]struct{}, 1024)
		for _, network := range networks {
			forEachIPv4Host(network.network, func(ip net.IP) bool {
				if ip.Equal(network.local) {
					return true
				}
				key := ipv4ToUint32(ip)
				if _, ok := seen[key]; ok {
					return true
				}
				seen[key] = struct{}{}

				select {
				case <-scanCtx.Done():
					return false
				case jobs <- append(net.IP(nil), ip...):
					return true
				}
			})
			if scanCtx.Err() != nil {
				return
			}
		}
	}()

	select {
	case ip := <-found:
		<-feedDone
		wg.Wait()
		return ip, nil
	case <-feedDone:
		wg.Wait()
		select {
		case ip := <-found:
			return ip, nil
		default:
			return nil, errReachableProxyNotFound
		}
	case <-ctx.Done():
		cancel()
		<-feedDone
		wg.Wait()
		return nil, ctx.Err()
	}
}

func localIPv4Networks(gatewayHint net.IP) ([]ipv4Network, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	networks := make([]ipv4Network, 0, len(interfaces))
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, err
		}
		for _, addr := range addrs {
			ip, ipNet, ok := ipv4AddrNetwork(addr)
			if !ok || !isUsefulIPv4Network(ipNet) {
				continue
			}
			networks = append(networks, ipv4Network{local: ip, network: ipNet})
		}
	}

	sort.SliceStable(networks, func(i, j int) bool {
		iHasGateway := gatewayHint != nil && networks[i].network.Contains(gatewayHint)
		jHasGateway := gatewayHint != nil && networks[j].network.Contains(gatewayHint)
		if iHasGateway != jHasGateway {
			return iHasGateway
		}
		iOnes, iBits := networks[i].network.Mask.Size()
		jOnes, jBits := networks[j].network.Mask.Size()
		if iBits != jBits {
			return iBits > jBits
		}
		if iOnes != jOnes {
			return iOnes > jOnes
		}
		return ipv4ToUint32(networks[i].local) < ipv4ToUint32(networks[j].local)
	})

	return networks, nil
}

func ipv4AddrNetwork(addr net.Addr) (net.IP, *net.IPNet, bool) {
	ipAddr, ok := addr.(*net.IPNet)
	if !ok {
		return nil, nil, false
	}
	ip := ipAddr.IP.To4()
	if ip == nil {
		return nil, nil, false
	}
	mask := ipAddr.Mask
	if len(mask) != net.IPv4len {
		mask = mask[len(mask)-net.IPv4len:]
	}
	networkIP := make(net.IP, net.IPv4len)
	copy(networkIP, ip)
	return append(net.IP(nil), ip...), &net.IPNet{IP: networkIP.Mask(mask), Mask: append(net.IPMask(nil), mask...)}, true
}

func isUsefulIPv4Network(network *net.IPNet) bool {
	if network == nil {
		return false
	}
	ones, bits := network.Mask.Size()
	return bits == 32 && ones < 31
}

func forEachIPv4Host(network *net.IPNet, fn func(net.IP) bool) {
	if network == nil {
		return
	}
	base := ipv4ToUint32(network.IP)
	mask := ipv4ToUint32(net.IP(network.Mask))
	start := base & mask
	end := start | ^mask
	if end <= start+1 {
		return
	}
	for value := start + 1; value < end; value++ {
		if !fn(uint32ToIPv4(value)) {
			return
		}
	}
}

func ipv4ToUint32(ip net.IP) uint32 {
	v4 := ip.To4()
	if v4 == nil {
		return 0
	}
	return uint32(v4[0])<<24 | uint32(v4[1])<<16 | uint32(v4[2])<<8 | uint32(v4[3])
}

func uint32ToIPv4(value uint32) net.IP {
	return net.IPv4(byte(value>>24), byte(value>>16), byte(value>>8), byte(value))
}
