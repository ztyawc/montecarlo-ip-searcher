package privatesocks

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfigFile(t *testing.T) {
	path := writeTestConfig(t, `{
		"server": "2001:db8::1",
		"port": 10800,
		"username": "1234567890123456789",
		"password": "private-password",
		"method": "0x80",
		"handshake_timeout": "12s"
	}`)

	cfg, err := LoadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint() != "[2001:db8::1]:10800" {
		t.Fatalf("unexpected endpoint %q", cfg.Endpoint())
	}
	if cfg.Username != testUsername || cfg.Password != testPassword {
		t.Fatal("credentials were not loaded")
	}
	method, err := ParseMethod(cfg.Method)
	if err != nil || method != Method80 {
		t.Fatalf("unexpected method 0x%02x, error=%v", method, err)
	}
	timeout, err := cfg.ParseHandshakeTimeout()
	if err != nil || timeout != 12*time.Second {
		t.Fatalf("unexpected timeout %s, error=%v", timeout, err)
	}
}

func TestParseMethod(t *testing.T) {
	tests := []struct {
		value   string
		method  byte
		wantErr bool
	}{
		{value: "0x80", method: Method80},
		{value: "80", method: Method80},
		{value: "0X82", method: Method82},
		{value: "82", method: Method82},
		{value: "0x81", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			method, err := ParseMethod(test.value)
			if test.wantErr {
				if err == nil {
					t.Fatalf("unsupported method %q was accepted", test.value)
				}
				return
			}
			if err != nil || method != test.method {
				t.Fatalf("ParseMethod(%q) = 0x%02x, %v; want 0x%02x", test.value, method, err, test.method)
			}
		})
	}
}

func TestLoadConfigFileRejectsUnknownField(t *testing.T) {
	path := writeTestConfig(t, `{"server":"127.0.0.1","port":10800,"pasword":"typo"}`)
	if _, err := LoadConfigFile(path); err == nil {
		t.Fatal("unknown field was accepted")
	}
}

func TestLoadConfigFileRejectsConflictingEndpoint(t *testing.T) {
	path := writeTestConfig(t, `{
		"address":"127.0.0.1:10800",
		"server":"127.0.0.1",
		"port":10800
	}`)
	if _, err := LoadConfigFile(path); err == nil {
		t.Fatal("conflicting endpoint fields were accepted")
	}
}

func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private-socks.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
