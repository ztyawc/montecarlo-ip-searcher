// Package privatesocks implements the private SOCKS5 0x80 and 0x82
// authentication protocols used by some accelerator endpoints.
package privatesocks

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

const (
	// Method80 is the private single-byte challenge authentication method.
	Method80 byte = 0x80
	// Method82 is the private four-byte challenge authentication method.
	Method82 byte = 0x82

	usernameLength        = 19
	authenticationVersion = 0x01
	socksVersion          = 0x05
	commandConnect        = 0x01
	defaultTimeout        = 10 * time.Second
)

var method82FixedData = [...]byte{
	0x14, 0x01, 0x01, 0x01, 0x02, 0x04, 0x00,
	0x00, 0x00, 0x00, 0x03, 0x02, 0x27, 0x10,
	0x04, 0x01, 0x01, 0x05, 0x02, 0x00, 0x04,
}

// Config configures a private SOCKS5 dialer.
type Config struct {
	Address          string
	Username         string
	Password         string
	Method           byte
	HandshakeTimeout time.Duration
}

// Dialer establishes TCP tunnels through a private SOCKS5 endpoint.
type Dialer struct {
	cfg       Config
	netDialer net.Dialer
}

// New creates a validated private SOCKS5 dialer.
func New(cfg Config) (*Dialer, error) {
	cfg.Address = strings.TrimSpace(cfg.Address)
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = defaultTimeout
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Dialer{
		cfg: cfg,
		netDialer: net.Dialer{
			Timeout:   cfg.HandshakeTimeout,
			KeepAlive: 30 * time.Second,
		},
	}, nil
}

// Validate validates the endpoint and authentication settings.
func (c Config) Validate() error {
	if c.Address == "" {
		return errors.New("private SOCKS address must not be empty")
	}
	host, portText, err := net.SplitHostPort(c.Address)
	if err != nil || host == "" {
		return fmt.Errorf("invalid private SOCKS address %q: expected host:port", c.Address)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid private SOCKS port %q", portText)
	}
	if c.Method != Method80 && c.Method != Method82 {
		return fmt.Errorf("unsupported private SOCKS method 0x%02x: supported methods are 0x80 and 0x82", c.Method)
	}
	if len(c.Username) != usernameLength {
		return fmt.Errorf("private SOCKS username must be exactly %d bytes", usernameLength)
	}
	if c.Password == "" {
		return errors.New("private SOCKS password must not be empty")
	}
	return nil
}

// DialContext implements the signature required by http.Transport.DialContext.
func (d *Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("private SOCKS only supports TCP, got %q", network)
	}

	connectRequest, err := buildConnectRequest(address)
	if err != nil {
		return nil, err
	}

	conn, err := d.netDialer.DialContext(ctx, "tcp", d.cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("connect private SOCKS endpoint: %w", err)
	}

	connected := false
	defer func() {
		if !connected {
			_ = conn.Close()
		}
	}()

	stopCancellation := context.AfterFunc(ctx, func() {
		_ = conn.Close()
	})
	defer stopCancellation()

	deadline := time.Now().Add(d.cfg.HandshakeTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("set private SOCKS handshake deadline: %w", err)
	}

	if err := writeXOR(conn, []byte{socksVersion, 0x01, d.cfg.Method}); err != nil {
		return nil, contextError(ctx, "send private SOCKS method", err)
	}

	challenge, err := d.readAuthenticationChallenge(conn)
	if err != nil {
		return nil, contextError(ctx, "read private SOCKS challenge", err)
	}

	authenticationRequest, err := d.buildAuthenticationRequest(challenge)
	if err != nil {
		return nil, err
	}
	if err := writeXOR(conn, authenticationRequest); err != nil {
		return nil, contextError(ctx, "send private SOCKS authentication", err)
	}

	var authenticationResponse [2]byte
	if _, err := io.ReadFull(conn, authenticationResponse[:]); err != nil {
		return nil, contextError(ctx, "read private SOCKS authentication response", err)
	}
	if authenticationResponse[0] != authenticationVersion || authenticationResponse[1] != 0x00 {
		return nil, fmt.Errorf(
			"private SOCKS authentication rejected: version=0x%02x status=0x%02x",
			authenticationResponse[0],
			authenticationResponse[1],
		)
	}

	if err := writeXOR(conn, connectRequest); err != nil {
		return nil, contextError(ctx, "send private SOCKS CONNECT request", err)
	}
	if err := readConnectResponse(conn); err != nil {
		return nil, contextError(ctx, "read private SOCKS CONNECT response", err)
	}

	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("clear private SOCKS handshake deadline: %w", err)
	}
	if !stopCancellation() && ctx.Err() != nil {
		return nil, ctx.Err()
	}

	connected = true
	return &clientXORConn{Conn: conn}, nil
}

func (d *Dialer) readAuthenticationChallenge(reader io.Reader) ([]byte, error) {
	switch d.cfg.Method {
	case Method80:
		var response [2]byte
		if _, err := io.ReadFull(reader, response[:]); err != nil {
			return nil, err
		}
		if response[0] != socksVersion {
			return nil, fmt.Errorf("invalid private SOCKS challenge version 0x%02x", response[0])
		}
		return response[1:], nil
	case Method82:
		var response [6]byte
		if _, err := io.ReadFull(reader, response[:]); err != nil {
			return nil, err
		}
		if response[0] != socksVersion {
			return nil, fmt.Errorf("invalid private SOCKS challenge version 0x%02x", response[0])
		}
		if response[1] != Method82 {
			return nil, fmt.Errorf("private SOCKS method 0x82 rejected: selected method=0x%02x", response[1])
		}
		return response[2:], nil
	default:
		return nil, fmt.Errorf("unsupported private SOCKS method 0x%02x", d.cfg.Method)
	}
}

func (d *Dialer) buildAuthenticationRequest(challenge []byte) ([]byte, error) {
	var key string
	var fixedData []byte
	switch d.cfg.Method {
	case Method80:
		if len(challenge) != 1 {
			return nil, fmt.Errorf("private SOCKS method 0x80 challenge must be 1 byte, got %d", len(challenge))
		}
		key = d.cfg.Username + d.cfg.Password
	case Method82:
		if len(challenge) != 4 {
			return nil, fmt.Errorf("private SOCKS method 0x82 challenge must be 4 bytes, got %d", len(challenge))
		}
		passwordDigest := md5.Sum([]byte(d.cfg.Password))
		key = d.cfg.Username + hex.EncodeToString(passwordDigest[:])
		fixedData = method82FixedData[:]
	default:
		return nil, fmt.Errorf("unsupported private SOCKS method 0x%02x", d.cfg.Method)
	}

	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write(challenge)
	signature := mac.Sum(nil)

	request := make([]byte, 0, 2+len(d.cfg.Username)+1+len(signature)+len(fixedData))
	request = append(request, authenticationVersion, byte(len(d.cfg.Username)))
	request = append(request, d.cfg.Username...)
	request = append(request, byte(len(signature)))
	request = append(request, signature...)
	request = append(request, fixedData...)
	return request, nil
}

func buildConnectRequest(address string) ([]byte, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid target address %q: %w", address, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid target port %q", portText)
	}

	request := []byte{socksVersion, commandConnect, 0x00}
	if addressValue, parseErr := netip.ParseAddr(host); parseErr == nil {
		if addressValue.Zone() != "" {
			return nil, fmt.Errorf("IPv6 zone identifiers are not supported in target address %q", address)
		}
		if addressValue.Is4() {
			request = append(request, 0x01)
			addressBytes := addressValue.As4()
			request = append(request, addressBytes[:]...)
		} else {
			request = append(request, 0x04)
			addressBytes := addressValue.As16()
			request = append(request, addressBytes[:]...)
		}
	} else {
		if len(host) == 0 || len(host) > 255 {
			return nil, fmt.Errorf("target domain length must be between 1 and 255 bytes")
		}
		request = append(request, 0x03, byte(len(host)))
		request = append(request, host...)
	}

	request = append(request, byte(port>>8), byte(port))
	return request, nil
}

func readConnectResponse(conn net.Conn) error {
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return err
	}
	if header[0] != socksVersion {
		return fmt.Errorf("invalid SOCKS5 response version 0x%02x", header[0])
	}
	if header[1] != 0x00 {
		return fmt.Errorf("SOCKS5 CONNECT rejected with status 0x%02x", header[1])
	}
	if header[2] != 0x00 {
		return fmt.Errorf("invalid SOCKS5 reserved byte 0x%02x", header[2])
	}

	switch header[3] {
	case 0x01:
		return discardFull(conn, 4+2)
	case 0x04:
		return discardFull(conn, 16+2)
	case 0x03:
		var domainLength [1]byte
		if _, err := io.ReadFull(conn, domainLength[:]); err != nil {
			return err
		}
		return discardFull(conn, int(domainLength[0])+2)
	default:
		return fmt.Errorf("unsupported SOCKS5 response address type 0x%02x", header[3])
	}
}

func discardFull(reader io.Reader, length int) error {
	_, err := io.CopyN(io.Discard, reader, int64(length))
	return err
}

func writeXOR(writer io.Writer, payload []byte) error {
	obfuscated := make([]byte, len(payload))
	for index, value := range payload {
		obfuscated[index] = value ^ 0xFF
	}
	_, err := writeFull(writer, obfuscated)
	return err
}

func writeFull(writer io.Writer, payload []byte) (int, error) {
	written := 0
	for written < len(payload) {
		count, err := writer.Write(payload[written:])
		written += count
		if err != nil {
			return written, err
		}
		if count == 0 {
			return written, io.ErrUnexpectedEOF
		}
	}
	return written, nil
}

func contextError(ctx context.Context, operation string, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return fmt.Errorf("%s: %w", operation, contextErr)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

// clientXORConn obfuscates every client-to-server byte while leaving
// server-to-client bytes unchanged.
type clientXORConn struct {
	net.Conn
}

func (c *clientXORConn) Write(payload []byte) (int, error) {
	obfuscated := make([]byte, len(payload))
	for index, value := range payload {
		obfuscated[index] = value ^ 0xFF
	}
	return c.Conn.Write(obfuscated)
}
