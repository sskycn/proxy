package proxy

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	xreality "github.com/xtls/reality"
)

type realityServer struct {
	cfg *xreality.Config
}

func newRealityServer(cfg config) (*realityServer, error) {
	realityCfg, err := buildRealityServerConfig(cfg)
	if err != nil {
		return nil, err
	}
	xreality.DetectPostHandshakeRecordsLens(realityCfg)
	return &realityServer{cfg: realityCfg}, nil
}

func (s *realityServer) accept(ctx context.Context, conn net.Conn) (net.Conn, error) {
	if s == nil || s.cfg == nil {
		return conn, nil
	}
	wrapped, err := xreality.Server(ctx, conn, s.cfg)
	if err != nil {
		return nil, err
	}
	return wrapped, nil
}

func buildRealityServerConfig(cfg config) (*xreality.Config, error) {
	privateKey, err := parseRealityKey(cfg.RealityPrivateKey, "REALITY private key")
	if err != nil {
		return nil, err
	}
	serverNames := normalizeRealityServerNames(cfg)
	if len(serverNames) == 0 {
		return nil, errors.New("REALITY server mode requires at least one server name")
	}
	shortIDs, err := normalizeRealityShortIDs(cfg)
	if err != nil {
		return nil, err
	}
	dest := strings.TrimSpace(cfg.RealityDest)
	if dest == "" {
		firstName, err := firstRealityServerName(serverNames)
		if err != nil {
			return nil, err
		}
		dest = net.JoinHostPort(firstName, "443")
	}
	if _, _, err := net.SplitHostPort(dest); err != nil {
		return nil, fmt.Errorf("invalid REALITY dest %q: %w", dest, err)
	}
	dialer := net.Dialer{
		Timeout:   cfg.DialTimeout,
		KeepAlive: 30 * time.Second,
	}
	return &xreality.Config{
		DialContext:            dialer.DialContext,
		Type:                   "tcp",
		Dest:                   dest,
		PrivateKey:             privateKey,
		ServerNames:            serverNames,
		ShortIds:               shortIDs,
		SessionTicketsDisabled: true,
	}, nil
}

func firstRealityServerName(names map[string]bool) (string, error) {
	for name := range names {
		return name, nil
	}
	return "", errors.New("REALITY server mode requires at least one server name")
}

func normalizeRealityServerNames(cfg config) map[string]bool {
	names := make(map[string]bool)
	for _, name := range cfg.RealityServerNames {
		trimmed := strings.ToLower(strings.TrimSpace(name))
		if trimmed != "" {
			names[trimmed] = true
		}
	}
	if trimmed := strings.ToLower(strings.TrimSpace(cfg.RealityServerName)); trimmed != "" {
		names[trimmed] = true
	}
	return names
}

func normalizeRealityShortIDs(cfg config) (map[[8]byte]bool, error) {
	values := cfg.RealityShortIDs
	if strings.TrimSpace(cfg.RealityShortID) != "" || len(values) == 0 {
		values = append(values, cfg.RealityShortID)
	}
	shortIDs := make(map[[8]byte]bool, len(values))
	for _, value := range values {
		decoded, err := parseRealityShortID(value)
		if err != nil {
			return nil, err
		}
		var padded [8]byte
		copy(padded[:], decoded)
		shortIDs[padded] = true
	}
	return shortIDs, nil
}

func parseRealityKey(value string, name string) ([]byte, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, fmt.Errorf("%s is required", name)
	}
	encodings := []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	}
	var decodeErr error
	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(trimmed)
		if err == nil {
			if len(decoded) != 32 {
				return nil, fmt.Errorf("invalid %s length: %d", name, len(decoded))
			}
			return decoded, nil
		}
		decodeErr = err
	}
	decoded, err := hex.DecodeString(trimmed)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", name, errors.Join(decodeErr, err))
	}
	if len(decoded) != 32 {
		return nil, fmt.Errorf("invalid %s length: %d", name, len(decoded))
	}
	return decoded, nil
}
