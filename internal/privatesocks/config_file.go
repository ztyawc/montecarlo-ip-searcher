package privatesocks

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// FileConfig is the cross-platform JSON representation of private SOCKS
// settings. Server and Port are preferred; Address is accepted as a compact
// host:port alternative.
type FileConfig struct {
	Address          string `json:"address,omitempty"`
	Server           string `json:"server,omitempty"`
	Port             int    `json:"port,omitempty"`
	Username         string `json:"username,omitempty"`
	Password         string `json:"password,omitempty"`
	Method           string `json:"method,omitempty"`
	HandshakeTimeout string `json:"handshake_timeout,omitempty"`
}

// LoadConfigFile reads a strict JSON configuration file. Unknown fields and
// trailing JSON values are rejected so misspelled security-sensitive settings
// cannot be silently ignored.
func LoadConfigFile(path string) (FileConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return FileConfig{}, fmt.Errorf("open private SOCKS config: %w", err)
	}
	defer file.Close()

	var cfg FileConfig
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return FileConfig{}, fmt.Errorf("parse private SOCKS config: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return FileConfig{}, err
	}

	cfg.Address = strings.TrimSpace(cfg.Address)
	cfg.Server = strings.TrimSpace(cfg.Server)
	cfg.Method = strings.TrimSpace(cfg.Method)
	cfg.HandshakeTimeout = strings.TrimSpace(cfg.HandshakeTimeout)

	if cfg.Address != "" && (cfg.Server != "" || cfg.Port != 0) {
		return FileConfig{}, errors.New("private SOCKS config must use either address or server/port, not both")
	}
	if (cfg.Server == "") != (cfg.Port == 0) {
		return FileConfig{}, errors.New("private SOCKS config server and port must be provided together")
	}
	if cfg.Port < 0 || cfg.Port > 65535 {
		return FileConfig{}, fmt.Errorf("private SOCKS config port must be in 1..65535, got %d", cfg.Port)
	}
	if cfg.Method != "" {
		if _, err := ParseMethod(cfg.Method); err != nil {
			return FileConfig{}, err
		}
	}
	if cfg.HandshakeTimeout != "" {
		timeout, err := time.ParseDuration(cfg.HandshakeTimeout)
		if err != nil || timeout <= 0 {
			return FileConfig{}, fmt.Errorf(
				"invalid private SOCKS handshake_timeout %q: expected a positive duration such as 10s",
				cfg.HandshakeTimeout,
			)
		}
	}

	return cfg, nil
}

// Endpoint returns the configured proxy endpoint. An empty endpoint is allowed
// so a command-line address can be combined with credentials from the file.
func (c FileConfig) Endpoint() string {
	if c.Address != "" {
		return c.Address
	}
	if c.Server == "" || c.Port == 0 {
		return ""
	}
	return net.JoinHostPort(c.Server, strconv.Itoa(c.Port))
}

// ParseMethod parses a private authentication method.
func ParseMethod(value string) (byte, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "0x80", "80":
		return Method80, nil
	case "0x82", "82":
		return Method82, nil
	default:
		return 0, fmt.Errorf("unsupported private SOCKS method %q: supported methods are 0x80 and 0x82", value)
	}
}

// ParseHandshakeTimeout parses the optional file duration.
func (c FileConfig) ParseHandshakeTimeout() (time.Duration, error) {
	if c.HandshakeTimeout == "" {
		return 0, nil
	}
	timeout, err := time.ParseDuration(c.HandshakeTimeout)
	if err != nil || timeout <= 0 {
		return 0, fmt.Errorf("invalid private SOCKS handshake_timeout %q", c.HandshakeTimeout)
	}
	return timeout, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("parse private SOCKS config trailing data: %w", err)
	}
	return errors.New("private SOCKS config must contain exactly one JSON object")
}
