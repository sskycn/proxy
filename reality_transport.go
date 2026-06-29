package proxy

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/crypto/hkdf"
)

const (
	realityVersionX = byte(26)
	realityVersionY = byte(3)
	realityVersionZ = byte(27)
)

type realityClientConn struct {
	*utls.UConn
	authKey    []byte
	serverName string
	verified   bool
}

func dialReality(ctx context.Context, raw net.Conn, cfg config) (net.Conn, error) {
	realityCfg, err := buildRealityClientConfig(cfg)
	if err != nil {
		return nil, err
	}
	client := &realityClientConn{}
	utlsCfg := &utls.Config{
		VerifyPeerCertificate:  client.verifyPeerCertificate,
		ServerName:             realityCfg.serverName,
		InsecureSkipVerify:     true,
		SessionTicketsDisabled: true,
	}
	client.serverName = utlsCfg.ServerName
	client.UConn = utls.UClient(raw, utlsCfg, realityCfg.fingerprint)
	if err := client.buildRealityHandshake(realityCfg); err != nil {
		return nil, err
	}
	if err := client.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	if !client.verified {
		return nil, errors.New("REALITY verification failed: received real or invalid certificate")
	}
	return client, nil
}

type realityClientConfig struct {
	serverName  string
	fingerprint utls.ClientHelloID
	publicKey   []byte
	shortID     []byte
}

func buildRealityClientConfig(cfg config) (realityClientConfig, error) {
	serverName := strings.TrimSpace(cfg.RealityServerName)
	if serverName == "" {
		return realityClientConfig{}, errors.New("REALITY server name is required")
	}
	publicKeyText := strings.TrimSpace(cfg.RealityPublicKey)
	if publicKeyText == "" {
		return realityClientConfig{}, errors.New("REALITY public key is required")
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(publicKeyText)
	if err != nil {
		return realityClientConfig{}, fmt.Errorf("decode REALITY public key: %w", err)
	}
	if len(publicKey) != 32 {
		return realityClientConfig{}, fmt.Errorf("invalid REALITY public key length: %d", len(publicKey))
	}
	shortID, err := parseRealityShortID(cfg.RealityShortID)
	if err != nil {
		return realityClientConfig{}, err
	}
	fingerprint, err := realityFingerprint(cfg.RealityFingerprint)
	if err != nil {
		return realityClientConfig{}, err
	}
	return realityClientConfig{
		serverName:  serverName,
		fingerprint: fingerprint,
		publicKey:   publicKey,
		shortID:     shortID,
	}, nil
}

func parseRealityShortID(value string) ([]byte, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	if len(trimmed) > 16 {
		return nil, errors.New("REALITY shortId is too long")
	}
	if len(trimmed)%2 != 0 {
		return nil, errors.New("REALITY shortId must have an even hex length")
	}
	decoded, err := hex.DecodeString(trimmed)
	if err != nil {
		return nil, fmt.Errorf("decode REALITY shortId: %w", err)
	}
	if len(decoded) > 8 {
		return nil, errors.New("REALITY shortId decoded length is too long")
	}
	return decoded, nil
}

func realityFingerprint(value string) (utls.ClientHelloID, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "chrome":
		return utls.HelloChrome_Auto, nil
	case "firefox":
		return utls.HelloFirefox_Auto, nil
	case "safari":
		return utls.HelloSafari_Auto, nil
	case "ios":
		return utls.HelloIOS_Auto, nil
	case "android":
		return utls.HelloAndroid_11_OkHttp, nil
	case "edge":
		return utls.HelloEdge_Auto, nil
	case "360":
		return utls.Hello360_Auto, nil
	case "qq":
		return utls.HelloQQ_Auto, nil
	default:
		return utls.ClientHelloID{}, fmt.Errorf("unsupported REALITY fingerprint %q", value)
	}
}

func (c *realityClientConn) buildRealityHandshake(cfg realityClientConfig) error {
	if err := c.BuildHandshakeState(); err != nil {
		return err
	}
	hello := c.HandshakeState.Hello
	if len(hello.Raw) < 71 {
		return errors.New("REALITY ClientHello is too short")
	}
	hello.SessionId = make([]byte, 32)
	copy(hello.Raw[39:], hello.SessionId)
	hello.SessionId[0] = realityVersionX
	hello.SessionId[1] = realityVersionY
	hello.SessionId[2] = realityVersionZ
	hello.SessionId[3] = 0
	binary.BigEndian.PutUint32(hello.SessionId[4:], uint32(time.Now().Unix()))
	copy(hello.SessionId[8:], cfg.shortID)

	publicKey, err := ecdh.X25519().NewPublicKey(cfg.publicKey)
	if err != nil {
		return fmt.Errorf("parse REALITY public key: %w", err)
	}
	keyShare := c.HandshakeState.State13.KeyShareKeys
	if keyShare == nil {
		return errors.New("REALITY fingerprint does not provide TLS 1.3 key share")
	}
	ecdhe := keyShare.Ecdhe
	if ecdhe == nil {
		ecdhe = keyShare.MlkemEcdhe
	}
	if ecdhe == nil {
		return errors.New("REALITY fingerprint does not support TLS 1.3 ECDHE")
	}
	authKey, err := ecdhe.ECDH(publicKey)
	if err != nil {
		return fmt.Errorf("REALITY ECDH: %w", err)
	}
	if authKey == nil {
		return errors.New("REALITY shared key is empty")
	}
	c.authKey = make([]byte, len(authKey))
	copy(c.authKey, authKey)
	if _, err := hkdf.New(sha256.New, c.authKey, hello.Random[:20], []byte("REALITY")).Read(c.authKey); err != nil {
		return err
	}
	block, err := aes.NewCipher(c.authKey)
	if err != nil {
		return err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	aead.Seal(hello.SessionId[:0], hello.Random[20:], hello.SessionId[:16], hello.Raw)
	copy(hello.Raw[39:], hello.SessionId)
	return nil
}

func (c *realityClientConn) verifyPeerCertificate(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	certs := make([]*x509.Certificate, 0, len(rawCerts))
	for _, raw := range rawCerts {
		cert, err := x509.ParseCertificate(raw)
		if err != nil {
			return err
		}
		certs = append(certs, cert)
	}
	if len(certs) == 0 {
		return errors.New("REALITY peer certificate is empty")
	}
	if pub, ok := certs[0].PublicKey.(ed25519.PublicKey); ok {
		h := hmac.New(sha512.New, c.authKey)
		if _, err := h.Write(pub); err != nil {
			return err
		}
		if bytes.Equal(h.Sum(nil), certs[0].Signature) {
			c.verified = true
			return nil
		}
	}
	opts := x509.VerifyOptions{
		DNSName:       c.serverName,
		Intermediates: x509.NewCertPool(),
	}
	for _, cert := range certs[1:] {
		opts.Intermediates.AddCert(cert)
	}
	if _, err := certs[0].Verify(opts); err != nil {
		return err
	}
	return nil
}
