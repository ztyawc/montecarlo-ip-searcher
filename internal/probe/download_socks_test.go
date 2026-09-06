package probe

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/privatesocks"
)

const downloadSOCKSUsername = "1234567890123456789"
const downloadSOCKSPassword = "local-download-test"

func TestDownloadThroughPrivateSOCKSPreservesURLAndClosesConnection(t *testing.T) {
	for _, http2 := range []bool{false, true} {
		for _, ip := range []netip.Addr{testIP, netip.MustParseAddr("2001:db8::7")} {
			t.Run(fmt.Sprintf("HTTP2=%v/%s", http2, ip), func(t *testing.T) {
				type observation struct {
					host, uri, path, rawPath, query, sni string
					protocol                             int
				}
				observed := make(chan observation, 1)
				origin, roots := localTLSServer(t, http2, func(w http.ResponseWriter, r *http.Request) {
					observed <- observation{r.Host, r.RequestURI, r.URL.Path, r.URL.RawPath, r.URL.RawQuery, r.TLS.ServerName, r.ProtoMajor}
					_, _ = io.WriteString(w, strings.Repeat("x", 128))
				})
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = listener.Close() })
				proxyDone := make(chan error, 1)
				go func() {
					proxyDone <- serveDownloadSOCKS80(listener, origin.Listener.Addr().String(), ip)
				}()
				dialer, err := privatesocks.New(privatesocks.Config{
					Address: listener.Addr().String(), Username: downloadSOCKSUsername, Password: downloadSOCKSPassword,
					Method: privatesocks.Method80, HandshakeTimeout: 2 * time.Second,
				})
				if err != nil {
					t.Fatal(err)
				}
				counts := &connectionCounts{}
				p := NewDownloadProber(DownloadConfig{
					URL: "https://example.com:8443/a%2Fb?sig=x%2Fy", Timeout: 2 * time.Second, Bytes: 64, MinBytes: 32,
					DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
						conn, err := dialer.DialContext(ctx, network, address)
						if err != nil {
							return nil, err
						}
						counts.mu.Lock()
						counts.accepted++
						counts.open++
						counts.peak = max(counts.peak, counts.open)
						counts.mu.Unlock()
						return &countedConn{Conn: conn, counts: counts}, nil
					},
				})
				p.client.Transport.(*http.Transport).TLSClientConfig.RootCAs = roots
				defer p.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				result := p.Download(ctx, ip)
				if !result.OK || result.IP != ip || result.Bytes != 64 {
					t.Fatalf("private SOCKS download failed: %+v", result)
				}
				assertConnectionsClosed(t, counts)
				if _, peak, accepted := counts.snapshot(); peak != 1 || accepted != 1 {
					t.Fatalf("unexpected proxy connections: peak=%d accepted=%d", peak, accepted)
				}
				select {
				case got := <-observed:
					if got.host != "example.com:8443" || got.sni != "example.com" || got.uri != "/a%2Fb?sig=x%2Fy" ||
						got.path != "/a/b" || got.rawPath != "/a%2Fb" || got.query != "sig=x%2Fy" || (got.protocol == 2) != http2 {
						t.Errorf("origin received changed URL or TLS identity: %+v", got)
					}
				default:
					t.Fatal("local TLS origin received no download request")
				}
				// The fake proxy waits for EOF/reset from the client. This checks the
				// actual TCP peer, beyond the counted Close call above.
				select {
				case err := <-proxyDone:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("private SOCKS TCP connection remained open after download")
				}
			})
		}
	}
}

// The private protocol XORs client-to-server traffic, including tunneled TLS;
// server-to-client bytes are unmodified. Only the loopback origin is dialed.
type downloadSOCKSXORReader struct{ io.Reader }

func (r downloadSOCKSXORReader) Read(payload []byte) (int, error) {
	n, err := r.Reader.Read(payload)
	for i := 0; i < n; i++ {
		payload[i] ^= 0xff
	}
	return n, err
}

func serveDownloadSOCKS80(listener net.Listener, originAddress string, candidate netip.Addr) error {
	conn, err := listener.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	decoded := downloadSOCKSXORReader{conn}
	readPacket := func(length int) ([]byte, error) {
		packet := make([]byte, length)
		_, err := io.ReadFull(decoded, packet)
		return packet, err
	}
	writePacket := func(packet []byte) error {
		_, err := io.Copy(conn, bytes.NewReader(packet))
		return err
	}
	method, err := readPacket(3)
	if err != nil {
		return err
	}
	if !bytes.Equal(method, []byte{5, 1, privatesocks.Method80}) {
		return fmt.Errorf("unexpected private SOCKS method packet: %x", method)
	}
	challenge := []byte{0x7a}
	if err := writePacket([]byte{5, challenge[0]}); err != nil {
		return err
	}
	auth, err := readPacket(54)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, []byte(downloadSOCKSUsername+downloadSOCKSPassword))
	_, _ = mac.Write(challenge)
	if auth[0] != 1 || auth[1] != 19 || string(auth[2:21]) != downloadSOCKSUsername || auth[21] != 32 ||
		!hmac.Equal(auth[22:], mac.Sum(nil)) {
		return fmt.Errorf("invalid private SOCKS authentication")
	}
	if err := writePacket([]byte{1, 0}); err != nil {
		return err
	}
	atyp := byte(1)
	if candidate.Is6() {
		atyp = 4
	}
	wantConnect := append([]byte{5, 1, 0, atyp}, candidate.AsSlice()...)
	wantConnect = binary.BigEndian.AppendUint16(wantConnect, 8443)
	connect, err := readPacket(len(wantConnect))
	if err != nil {
		return err
	}
	if !bytes.Equal(connect, wantConnect) {
		return fmt.Errorf("private SOCKS CONNECT changed candidate IP or port: got=%x want=%x", connect, wantConnect)
	}
	upstream, err := net.DialTimeout("tcp", originAddress, time.Second)
	if err != nil {
		return err
	}
	defer upstream.Close()
	if err := upstream.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	if err := writePacket([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return err
	}
	reverseDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(conn, upstream)
		close(reverseDone)
	}()
	_, err = io.Copy(upstream, decoded)
	_ = upstream.Close()
	_ = conn.Close()
	<-reverseDone
	if err != nil && !errors.Is(err, syscall.ECONNRESET) {
		return fmt.Errorf("private SOCKS did not observe client disconnect: %w", err)
	}
	return nil
}
