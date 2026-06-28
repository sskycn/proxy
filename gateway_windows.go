//go:build windows

package main

import (
	"net"
	"os/exec"
)

func discoverDefaultGateway() (net.IP, error) {
	output, err := exec.Command("route", "print", "-4", "0.0.0.0").Output()
	if err != nil {
		return nil, err
	}
	return parseWindowsRoutePrint(string(output))
}
