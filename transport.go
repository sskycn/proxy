package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

const tunnelContentType = "application/octet-stream"

type addrString struct {
	network string
	address string
}

func (a addrString) Network() string { return a.network }
func (a addrString) String() string  { return a.address }

type tunnelHTTPConn struct {
	reader     io.ReadCloser
	writer     io.Writer
	closeFn    func() error
	localAddr  net.Addr
	remoteAddr net.Addr
}

func (c *tunnelHTTPConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func (c *tunnelHTTPConn) Write(p []byte) (int, error) {
	n, err := c.writer.Write(p)
	if flusher, ok := c.writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return n, err
}

func (c *tunnelHTTPConn) Close() error {
	var closeErr error
	if c.closeFn != nil {
		closeErr = c.closeFn()
	}
	if c.reader != nil {
		return errors.Join(closeErr, c.reader.Close())
	}
	return closeErr
}

func (c *tunnelHTTPConn) LocalAddr() net.Addr {
	return c.localAddr
}

func (c *tunnelHTTPConn) RemoteAddr() net.Addr {
	return c.remoteAddr
}

func (c *tunnelHTTPConn) SetDeadline(time.Time) error {
	return nil
}

func (c *tunnelHTTPConn) SetReadDeadline(time.Time) error {
	return nil
}

func (c *tunnelHTTPConn) SetWriteDeadline(time.Time) error {
	return nil
}

type flushWriter struct {
	w http.ResponseWriter
}

func (w flushWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	if flusher, ok := w.w.(http.Flusher); ok {
		flusher.Flush()
	}
	return n, err
}

type websocketNetConn struct {
	conn       *websocket.Conn
	reader     io.Reader
	localAddr  net.Addr
	remoteAddr net.Addr
}

func (c *websocketNetConn) Read(p []byte) (int, error) {
	for {
		if c.reader == nil {
			messageType, reader, err := c.conn.NextReader()
			if err != nil {
				return 0, err
			}
			if messageType != websocket.BinaryMessage && messageType != websocket.TextMessage {
				continue
			}
			c.reader = reader
		}
		n, err := c.reader.Read(p)
		if errors.Is(err, io.EOF) {
			c.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *websocketNetConn) Write(p []byte) (int, error) {
	writer, err := c.conn.NextWriter(websocket.BinaryMessage)
	if err != nil {
		return 0, err
	}
	n, writeErr := writer.Write(p)
	closeErr := writer.Close()
	return n, errors.Join(writeErr, closeErr)
}

func (c *websocketNetConn) Close() error {
	return c.conn.Close()
}

func (c *websocketNetConn) LocalAddr() net.Addr {
	return c.localAddr
}

func (c *websocketNetConn) RemoteAddr() net.Addr {
	return c.remoteAddr
}

func (c *websocketNetConn) SetDeadline(t time.Time) error {
	return errors.Join(c.conn.SetReadDeadline(t), c.conn.SetWriteDeadline(t))
}

func (c *websocketNetConn) SetReadDeadline(t time.Time) error {
	return c.conn.SetReadDeadline(t)
}

func (c *websocketNetConn) SetWriteDeadline(t time.Time) error {
	return c.conn.SetWriteDeadline(t)
}

func runHTTPTunnelServer(ctx context.Context, cfg config, log io.Writer) error {
	mux := http.NewServeMux()
	server := &proxyServer{
		cfg: cfg,
		dialer: net.Dialer{
			Timeout:   cfg.DialTimeout,
			KeepAlive: 30 * time.Second,
		},
		log: log,
	}
	server.bufferPool.New = func() any {
		buf := make([]byte, cfg.BufferSize)
		return &buf
	}
	mux.HandleFunc(cfg.TunnelPath, func(w http.ResponseWriter, r *http.Request) {
		server.handleHTTPTunnelRequest(r.Context(), w, r)
	})

	if cfg.TunnelTransport == tunnelTransportH3 {
		return runHTTP3TunnelServer(ctx, cfg, mux, log)
	}
	return runHTTP12TunnelServer(ctx, cfg, mux, log)
}

func runHTTP12TunnelServer(ctx context.Context, cfg config, handler http.Handler, log io.Writer) error {
	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: cfg.DialTimeout,
	}
	h2Server := &http2.Server{}
	if cfg.TunnelTransport == tunnelTransportH2 && strings.TrimSpace(cfg.TunnelTLSCert) == "" && strings.TrimSpace(cfg.TunnelTLSKey) == "" {
		server.Handler = h2c.NewHandler(handler, h2Server)
	} else if cfg.TunnelTransport == tunnelTransportH2 {
		if err := http2.ConfigureServer(server, h2Server); err != nil {
			return err
		}
	}

	if err := logf(log, "tunnel server listening on %s via %s path %s\n", cfg.ListenAddr, cfg.TunnelTransport, cfg.TunnelPath); err != nil {
		return err
	}

	errCh := make(chan error, 1)
	go func() {
		if strings.TrimSpace(cfg.TunnelTLSCert) != "" || strings.TrimSpace(cfg.TunnelTLSKey) != "" {
			errCh <- server.ListenAndServeTLS(cfg.TunnelTLSCert, cfg.TunnelTLSKey)
			return
		}
		errCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		err := <-errCh
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return errors.Join(ctx.Err(), shutdownErr, err)
		}
		if shutdownErr != nil {
			return errors.Join(ctx.Err(), shutdownErr)
		}
		return nil
	}
}

func runHTTP3TunnelServer(ctx context.Context, cfg config, handler http.Handler, log io.Writer) error {
	if strings.TrimSpace(cfg.TunnelTLSCert) == "" || strings.TrimSpace(cfg.TunnelTLSKey) == "" {
		return errors.New("HTTP/3 tunnel server requires --tls-cert and --tls-key")
	}
	server := &http3.Server{
		Addr:    cfg.ListenAddr,
		Handler: handler,
	}
	if err := logf(log, "tunnel server listening on %s via %s path %s\n", cfg.ListenAddr, cfg.TunnelTransport, cfg.TunnelPath); err != nil {
		return err
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServeTLS(cfg.TunnelTLSCert, cfg.TunnelTLSKey)
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		closeErr := server.Close()
		err := <-errCh
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return errors.Join(ctx.Err(), closeErr, err)
		}
		if closeErr != nil {
			return errors.Join(ctx.Err(), closeErr)
		}
		return nil
	}
}

func (s *proxyServer) handleHTTPTunnelRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var err error
	switch s.cfg.TunnelTransport {
	case tunnelTransportWS:
		err = s.handleWebSocketTunnel(ctx, w, r)
	case tunnelTransportH2, tunnelTransportH3:
		err = s.handleHTTPStreamTunnel(ctx, w, r)
	default:
		http.Error(w, "unsupported tunnel transport", http.StatusBadRequest)
		return
	}
	if err != nil && s.cfg.Verbose {
		if logErr := logf(s.log, "http tunnel error for %s: %v\n", r.RemoteAddr, err); logErr != nil {
			return
		}
	}
}

func (s *proxyServer) handleWebSocketTunnel(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return nil
	}
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}
	wsConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return err
	}
	underlying := wsConn.UnderlyingConn()
	conn := &websocketNetConn{
		conn: wsConn,
		localAddr: addrString{
			network: "ws",
			address: s.cfg.ListenAddr,
		},
		remoteAddr: addrString{
			network: "ws",
			address: r.RemoteAddr,
		},
	}
	if underlying != nil {
		conn.localAddr = underlying.LocalAddr()
		conn.remoteAddr = underlying.RemoteAddr()
	}
	return s.handleTunnelConnError(ctx, conn)
}

func (s *proxyServer) handleHTTPStreamTunnel(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return nil
	}
	if s.cfg.TunnelTransport == tunnelTransportH2 && r.ProtoMajor != 2 {
		http.Error(w, "HTTP/2 required", http.StatusUpgradeRequired)
		return nil
	}
	if s.cfg.TunnelTransport == tunnelTransportH3 && r.ProtoMajor != 3 {
		http.Error(w, "HTTP/3 required", http.StatusUpgradeRequired)
		return nil
	}
	controller := http.NewResponseController(w)
	if err := controller.EnableFullDuplex(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return err
	}
	w.Header().Set("Content-Type", tunnelContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	conn := &tunnelHTTPConn{
		reader: r.Body,
		writer: flushWriter{
			w: w,
		},
		localAddr: addrString{
			network: s.cfg.TunnelTransport,
			address: s.cfg.ListenAddr,
		},
		remoteAddr: addrString{
			network: s.cfg.TunnelTransport,
			address: r.RemoteAddr,
		},
	}
	return s.handleTunnelConnError(ctx, conn)
}

func (s *proxyServer) dialTunnelTransport(ctx context.Context) (net.Conn, error) {
	switch s.cfg.TunnelTransport {
	case tunnelTransportRaw:
		conn, err := s.dialer.DialContext(ctx, "tcp", s.cfg.ServerAddr)
		if err != nil {
			return nil, err
		}
		if s.cfg.TunnelSecurity == tunnelSecurityReality {
			realityConn, err := dialReality(ctx, conn, s.cfg)
			if err != nil {
				return nil, closeAfterError(conn, err)
			}
			return realityConn, nil
		}
		return conn, nil
	case tunnelTransportWS:
		return s.dialWebSocketTunnel(ctx)
	case tunnelTransportH2:
		return s.dialHTTPStreamTunnel(ctx, tunnelTransportH2)
	case tunnelTransportH3:
		return s.dialHTTPStreamTunnel(ctx, tunnelTransportH3)
	default:
		return nil, fmt.Errorf("unsupported tunnel transport %q", s.cfg.TunnelTransport)
	}
}

func (s *proxyServer) dialWebSocketTunnel(ctx context.Context) (net.Conn, error) {
	tunnelURL, err := buildTunnelURL(s.cfg, tunnelTransportWS)
	if err != nil {
		return nil, err
	}
	dialer := websocket.Dialer{
		HandshakeTimeout: s.cfg.DialTimeout,
	}
	if s.cfg.TunnelTLS || strings.EqualFold(tunnelURL.Scheme, "wss") {
		dialer.TLSClientConfig = tunnelTLSClientConfig(s.cfg)
	}
	wsConn, _, err := dialer.DialContext(ctx, tunnelURL.String(), nil)
	if err != nil {
		return nil, err
	}
	underlying := wsConn.UnderlyingConn()
	conn := &websocketNetConn{
		conn: wsConn,
		localAddr: addrString{
			network: "ws",
			address: "client",
		},
		remoteAddr: addrString{
			network: "ws",
			address: tunnelURL.Host,
		},
	}
	if underlying != nil {
		conn.localAddr = underlying.LocalAddr()
		conn.remoteAddr = underlying.RemoteAddr()
	}
	return conn, nil
}

func (s *proxyServer) dialHTTPStreamTunnel(ctx context.Context, transport string) (net.Conn, error) {
	tunnelURL, err := buildTunnelURL(s.cfg, transport)
	if err != nil {
		return nil, err
	}
	pipeReader, pipeWriter := io.Pipe()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tunnelURL.String(), pipeReader)
	if err != nil {
		closeErr := pipeReader.CloseWithError(err)
		return nil, errors.Join(err, closeErr)
	}
	request.Header.Set("Content-Type", tunnelContentType)
	request.Header.Set("Cache-Control", "no-store")

	roundTripper, closeTransport, err := s.tunnelRoundTripper(transport, tunnelURL)
	if err != nil {
		closeErr := pipeReader.CloseWithError(err)
		return nil, errors.Join(err, closeErr)
	}
	client := &http.Client{Transport: roundTripper}
	response, err := client.Do(request)
	if err != nil {
		closeErr := pipeReader.CloseWithError(err)
		return nil, errors.Join(err, closeErr, closeTransport())
	}
	if response.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
		closeErr := response.Body.Close()
		pipeErr := pipeWriter.Close()
		statusErr := fmt.Errorf("tunnel HTTP status %s: %s", response.Status, strings.TrimSpace(string(body)))
		return nil, errors.Join(statusErr, readErr, closeErr, pipeErr, closeTransport())
	}
	return &tunnelHTTPConn{
		reader: response.Body,
		writer: pipeWriter,
		closeFn: func() error {
			return errors.Join(pipeWriter.Close(), closeTransport())
		},
		localAddr: addrString{
			network: transport,
			address: "client",
		},
		remoteAddr: addrString{
			network: transport,
			address: tunnelURL.Host,
		},
	}, nil
}

func (s *proxyServer) tunnelRoundTripper(transport string, tunnelURL *url.URL) (http.RoundTripper, func() error, error) {
	switch transport {
	case tunnelTransportH2:
		rt := &http2.Transport{}
		if tunnelURL.Scheme == "https" {
			rt.TLSClientConfig = tunnelTLSClientConfig(s.cfg)
		} else {
			rt.AllowHTTP = true
			rt.DialTLSContext = func(ctx context.Context, network string, addr string, cfg *tls.Config) (net.Conn, error) {
				return s.dialer.DialContext(ctx, "tcp", addr)
			}
		}
		return rt, func() error {
			rt.CloseIdleConnections()
			return nil
		}, nil
	case tunnelTransportH3:
		rt := &http3.Transport{
			TLSClientConfig: tunnelTLSClientConfig(s.cfg),
		}
		return rt, rt.Close, nil
	default:
		return nil, nil, fmt.Errorf("unsupported HTTP tunnel transport %q", transport)
	}
}

func buildTunnelURL(cfg config, transport string) (*url.URL, error) {
	serverAddr := strings.TrimSpace(cfg.ServerAddr)
	if serverAddr == "" {
		return nil, errors.New("server address is required")
	}
	if strings.Contains(serverAddr, "://") {
		parsed, err := url.Parse(serverAddr)
		if err != nil {
			return nil, err
		}
		if parsed.Host == "" {
			return nil, fmt.Errorf("server address %q has no host", serverAddr)
		}
		if parsed.Path == "" {
			parsed.Path = normalizeTunnelPath(cfg.TunnelPath)
		}
		return parsed, validateTunnelURL(parsed, transport)
	}

	scheme := "tcp"
	switch transport {
	case tunnelTransportWS:
		scheme = "ws"
		if cfg.TunnelTLS {
			scheme = "wss"
		}
	case tunnelTransportH2:
		scheme = "http"
		if cfg.TunnelTLS {
			scheme = "https"
		}
	case tunnelTransportH3:
		scheme = "https"
	default:
		return nil, fmt.Errorf("unsupported tunnel transport %q", transport)
	}
	parsed := &url.URL{
		Scheme: scheme,
		Host:   serverAddr,
		Path:   normalizeTunnelPath(cfg.TunnelPath),
	}
	return parsed, validateTunnelURL(parsed, transport)
}

func validateTunnelURL(tunnelURL *url.URL, transport string) error {
	switch transport {
	case tunnelTransportWS:
		if tunnelURL.Scheme != "ws" && tunnelURL.Scheme != "wss" {
			return fmt.Errorf("websocket tunnel requires ws or wss URL, got %q", tunnelURL.Scheme)
		}
	case tunnelTransportH2:
		if tunnelURL.Scheme != "http" && tunnelURL.Scheme != "https" {
			return fmt.Errorf("HTTP/2 tunnel requires http or https URL, got %q", tunnelURL.Scheme)
		}
	case tunnelTransportH3:
		if tunnelURL.Scheme != "https" {
			return fmt.Errorf("HTTP/3 tunnel requires https URL, got %q", tunnelURL.Scheme)
		}
	default:
		return fmt.Errorf("unsupported tunnel transport %q", transport)
	}
	return nil
}

func tunnelTLSClientConfig(cfg config) *tls.Config {
	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         strings.TrimSpace(cfg.TunnelTLSServerName),
		InsecureSkipVerify: cfg.TunnelTLSInsecure,
	}
	return tlsConfig
}
