//go:build darwin

package main

import (
	"net"
	"os/exec"
)

func discoverDefaultGateway() (net.IP, error) {
	output, err := exec.Command("route", "-n", "get", "default").Output()
	if err != nil {
		return nil, err
	}
	return parseRouteGetDefault(string(output))
}
