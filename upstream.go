package main

import (
	"context"
	"net"
)

type upstreamDialer interface {
	dialTCP(ctx context.Context, req socksRequest) (net.Conn, string, error)
}

type socks5UpstreamDialer struct {
	server *proxyServer
}

type tunnelUpstreamDialer struct {
	server *proxyServer
}

func (s socks5UpstreamDialer) dialTCP(ctx context.Context, req socksRequest) (net.Conn, string, error) {
	return s.server.connectViaUpstreamSocks5(ctx, req)
}

func (t tunnelUpstreamDialer) dialTCP(ctx context.Context, req socksRequest) (net.Conn, string, error) {
	return t.server.connectViaTunnelTCP(ctx, req)
}

func (s *proxyServer) upstreamDialer() upstreamDialer {
	if s.cfg.Mode == proxyModeClient {
		return tunnelUpstreamDialer{server: s}
	}
	return socks5UpstreamDialer{server: s}
}
