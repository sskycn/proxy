//go:build !linux && !darwin && !windows

package main

import "net"

func discoverDefaultGateway() (net.IP, error) {
	return nil, errGatewayNotFound
}
