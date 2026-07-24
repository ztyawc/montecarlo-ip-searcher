package privatesocks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

const (
	testUsername = "1234567890123456789"
	testPassword = "private-password"
)

func TestDialContextMethod80(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- runFakeServer(listener)
	}()

	dialer, err := New(Config{
		Address:          listener.Addr().String(),
		Username:         testUsername,
		Password:         testPassword,
		Method:           Method80,
		HandshakeTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := dialer.DialContext(ctx, "tcp", "203.0.113.7:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("client-payload")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len("server-payload"))
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != "server-payload" {
		t.Fatalf("unexpected response %q", response)
	}

	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestBuildConnectRequest(t *testing.T) {
	tests := []struct {
		name    string
		address string
		want    []byte
	}{
		{
			name:    "IPv4",
			address: "192.0.2.1:443",
			want:    []byte{0x05, 0x01, 0x00, 0x01, 192, 0, 2, 1, 0x01, 0xBB},
		},
		{
			name:    "domain",
			address: "example.com:80",
			want: append(
				[]byte{0x05, 0x01, 0x00, 0x03, 11},
				append([]byte("example.com"), 0x00, 0x50)...,
			),
		},
		{
			name:    "IPv6",
			address: "[2001:db8::1]:443",
			want: []byte{
				0x05, 0x01, 0x00, 0x04,
				0x20, 0x01, 0x0D, 0xB8, 0, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0, 1,
				0x01, 0xBB,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := buildConnectRequest(test.address)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, test.want) {
				t.Fatalf("unexpected request\n got: %x\nwant: %x", got, test.want)
			}
		})
	}
}

func TestConfigValidation(t *testing.T) {
	valid := Config{
		Address:  "127.0.0.1:10800",
		Username: testUsername,
		Password: testPassword,
		Method:   Method80,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid configuration rejected: %v", err)
	}

	tests := []Config{
		{Address: "127.0.0.1", Username: testUsername, Password: testPassword, Method: Method80},
		{Address: "127.0.0.1:10800", Username: "short", Password: testPassword, Method: Method80},
		{Address: "127.0.0.1:10800", Username: testUsername, Method: Method80},
		{Address: "127.0.0.1:10800", Username: testUsername, Password: testPassword, Method: 0x82},
	}
	for index, test := range tests {
		if err := test.Validate(); err == nil {
			t.Fatalf("invalid configuration %d accepted", index)
		}
	}
}

func TestLiveDialContext(t *testing.T) {
	if os.Getenv("MCIS_PRIVATE_SOCKS_LIVE") != "1" {
		t.Skip("set MCIS_PRIVATE_SOCKS_LIVE=1 to run against a real endpoint")
	}

	dialer, err := New(Config{
		Address:          os.Getenv("MCIS_PRIVATE_SOCKS"),
		Username:         os.Getenv("MCIS_PRIVATE_SOCKS_USERNAME"),
		Password:         os.Getenv("MCIS_PRIVATE_SOCKS_PASSWORD"),
		Method:           Method80,
		HandshakeTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	target := os.Getenv("MCIS_PRIVATE_SOCKS_TEST_TARGET")
	if target == "" {
		target = "1.1.1.1:443"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if serverName := os.Getenv("MCIS_PRIVATE_SOCKS_TEST_SNI"); serverName != "" {
		tlsBaseConn := conn
		if os.Getenv("MCIS_PRIVATE_SOCKS_TEST_PLAIN_PAYLOAD") == "1" {
			tlsBaseConn = conn.(*clientXORConn).Conn
		}
		tlsConn := tls.Client(tlsBaseConn, &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: serverName,
		})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func runFakeServer(listener net.Listener) error {
	conn, err := listener.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}

	methodRequest, err := readXOR(conn, 3)
	if err != nil {
		return err
	}
	if !bytes.Equal(methodRequest, []byte{0x05, 0x01, Method80}) {
		return fmt.Errorf("unexpected method request %x", methodRequest)
	}

	const challenge = 0x7A
	if _, err := writeFull(conn, []byte{0x05, challenge}); err != nil {
		return err
	}

	authenticationRequest, err := readXOR(conn, 54)
	if err != nil {
		return err
	}
	if authenticationRequest[0] != 0x01 ||
		authenticationRequest[1] != 19 ||
		string(authenticationRequest[2:21]) != testUsername ||
		authenticationRequest[21] != 32 {
		return fmt.Errorf("invalid authentication packet %x", authenticationRequest[:22])
	}
	mac := hmac.New(sha256.New, []byte(testUsername+testPassword))
	_, _ = mac.Write([]byte{challenge})
	if !hmac.Equal(authenticationRequest[22:], mac.Sum(nil)) {
		return fmt.Errorf("invalid authentication signature")
	}

	if _, err := writeFull(conn, []byte{0x01, 0x00}); err != nil {
		return err
	}

	connectRequest, err := readXOR(conn, 10)
	if err != nil {
		return err
	}
	expectedConnect := []byte{0x05, 0x01, 0x00, 0x01, 203, 0, 113, 7, 0x01, 0xBB}
	if !bytes.Equal(connectRequest, expectedConnect) {
		return fmt.Errorf("unexpected CONNECT request %x", connectRequest)
	}

	if _, err := writeFull(conn, []byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return err
	}

	payload, err := readXOR(conn, len("client-payload"))
	if err != nil {
		return err
	}
	if string(payload) != "client-payload" {
		return fmt.Errorf("unexpected client payload %q", payload)
	}
	_, err = writeFull(conn, []byte("server-payload"))
	return err
}

func readXOR(reader io.Reader, length int) ([]byte, error) {
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	for index := range payload {
		payload[index] ^= 0xFF
	}
	return payload, nil
}
