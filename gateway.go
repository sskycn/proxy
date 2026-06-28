package main

import (
	"errors"
	"net"
	"strconv"
	"strings"
)

var errGatewayNotFound = errors.New("default gateway not found")

func parseLinuxDefaultGateway(routeTable string) (net.IP, error) {
	lines := strings.Split(routeTable, "\n")
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[1] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 32)
		if err != nil || flags&0x2 == 0 {
			continue
		}
		ip, err := parseLittleEndianIPv4Hex(fields[2])
		if err == nil && ip != nil {
			return ip, nil
		}
	}
	return nil, errGatewayNotFound
}

func parseLittleEndianIPv4Hex(value string) (net.IP, error) {
	if len(value) != 8 {
		return nil, errGatewayNotFound
	}
	n, err := strconv.ParseUint(value, 16, 32)
	if err != nil {
		return nil, err
	}
	return net.IPv4(byte(n), byte(n>>8), byte(n>>16), byte(n>>24)), nil
}

func parseRouteGetDefault(output string) (net.IP, error) {
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || strings.TrimSpace(key) != "gateway" {
			continue
		}
		ip := net.ParseIP(strings.TrimSpace(value))
		if ip != nil {
			return ip, nil
		}
	}
	return nil, errGatewayNotFound
}

func parseWindowsRoutePrint(output string) (net.IP, error) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "0.0.0.0" || fields[1] != "0.0.0.0" {
			continue
		}
		ip := net.ParseIP(fields[2])
		if ip != nil {
			return ip, nil
		}
	}
	return nil, errGatewayNotFound
}
