package probe

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestValidateDownloadURL(t *testing.T) {
	for _, raw := range []string{
		"https://example.com", "HTTPS://example.com:443/", "https://example.com:8443/a%2Fb?sig=x%2Fy&x=1&x=2",
		"https://example.com/?", "https://example.com/%23file", "https://example.com/文件.bin",
		"https://[2001:db8::1]:8443/file", "https://127.0.0.1/file", "https://example.com./file",
	} {
		if err := ValidateDownloadURL(raw); err != nil {
			t.Errorf("valid URL rejected: %s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"", "/relative", "http://example.com/file", "ftp://example.com/file", "https:example.com", "https:///file",
		"https://user:secret@example.com/file", "https://user:secret@example.com:bad/file", "https://example.com/file#part", "https://example.com/file#",
		"https://example.com:", "https://example.com:0/file", "https://example.com:65536/file", "https://example.com:-1/file", "https://example.com:abc/file",
		"https://[fe80::1%25eth0]/file", "https://2001:db8::1/file", "https://[not-an-ip]/file", "https://example..com/file", "https://example.com/%zz",
	} {
		if err := ValidateDownloadURL(raw); err == nil {
			t.Errorf("invalid URL accepted: %s", raw)
		} else if strings.Contains(err.Error(), "secret") {
			t.Errorf("validation error exposed credentials: %v", err)
		}
		p := NewDownloadProber(DownloadConfig{URL: raw, CustomURL: true})
		if raw == "" {
			// Empty URL intentionally selects the compatible host/path branch.
			p.Close()
			continue
		}
		p.client.Transport = testTransport(func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid custom URL issued a request")
			return nil, nil
		})
		result := p.Download(context.Background(), testIP)
		if result.OK || !strings.HasPrefix(result.Error, "invalid_download_url:") {
			t.Errorf("invalid URL did not fail locally: %+v", result)
		}
	}
}

func TestDownloadURLPreservesRequestAndPinsCandidate(t *testing.T) {
	for _, http2 := range []bool{false, true} {
		for _, tc := range []struct {
			name, url, host, requestURI, port string
			cap                               int64
		}{
			{"port and signed escaped path", "https://example.com:8443/folder%2Ffile%20name%23x?sig=a%2Fb%3D&x=1&x=2", "example.com:8443", "/folder%2Ffile%20name%23x?sig=a%2Fb%3D&x=1&x=2", "8443", 64},
			{"root", "https://example.com", "example.com", "/", "443", 0},
			{"root query", "https://example.com?token=a%2fb&value=1+2", "example.com", "/?token=a%2fb&value=1+2", "443", 0},
			{"explicit default port", "https://example.com:443/a%2fpart", "example.com:443", "/a%2fpart", "443", 64},
			{"empty query", "https://example.com/?", "example.com", "/?", "443", 0},
		} {
			for _, ip := range []netip.Addr{testIP, netip.MustParseAddr("2001:db8::1")} {
				t.Run(fmt.Sprintf("HTTP2=%v/%s/%s", http2, tc.name, ip), func(t *testing.T) {
					var mu sync.Mutex
					var gotHost, gotURI, gotSNI, gotDial string
					server, roots := localTLSServer(t, http2, func(w http.ResponseWriter, r *http.Request) {
						mu.Lock()
						gotHost, gotURI, gotSNI = r.Host, r.RequestURI, r.TLS.ServerName
						mu.Unlock()
						_, _ = io.WriteString(w, strings.Repeat("x", 128))
					})
					counts := &connectionCounts{}
					dial := counts.dialLocal(server.Listener.Addr().String())
					p := NewDownloadProber(DownloadConfig{
						URL: tc.url, Timeout: time.Second, Bytes: tc.cap, MinBytes: 32,
						SNI: "ignored.invalid", HostName: "ignored.invalid", Path: "/ignored",
						DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
							mu.Lock()
							gotDial = address
							mu.Unlock()
							return dial(ctx, network, address)
						},
					})
					p.client.Transport.(*http.Transport).TLSClientConfig.RootCAs = roots
					defer p.Close()
					result := p.Download(context.Background(), ip)
					wantBytes := tc.cap
					if wantBytes == 0 {
						wantBytes = 128
					}
					if !result.OK || result.IP != ip || result.Bytes != wantBytes {
						t.Fatalf("bad download result: %+v", result)
					}
					mu.Lock()
					if gotDial != net.JoinHostPort(ip.String(), tc.port) || gotHost != tc.host || gotURI != tc.requestURI || gotSNI != "example.com" {
						t.Errorf("dial=%q Host=%q URI=%q SNI=%q", gotDial, gotHost, gotURI, gotSNI)
					}
					mu.Unlock()
					assertConnectionsClosed(t, counts)
				})
			}
		}
	}
}

func TestDownloadURLIPv6OriginAuthority(t *testing.T) {
	p := NewDownloadProber(DownloadConfig{URL: "https://[2001:db8::a]:8443/file?x=1", Bytes: 64, MinBytes: 32})
	if got := p.client.Transport.(*http.Transport).TLSClientConfig.ServerName; got != "2001:db8::a" {
		t.Fatalf("SNI=%q", got)
	}
	p.client.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
		if r.Host != "[2001:db8::a]:8443" || r.URL.Host != "192.0.2.1:8443" || r.URL.RequestURI() != "/file?x=1" {
			t.Fatalf("unexpected IPv6 authority request: %+v", r)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 64))), ContentLength: 64, Header: make(http.Header)}, nil
	})
	if result := p.Download(context.Background(), testIP); !result.OK {
		t.Fatal(result.Error)
	}
}
