package probe

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type connectionCounts struct {
	mu                   sync.Mutex
	open, peak, accepted int
}

func (c *connectionCounts) snapshot() (open, peak, accepted int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.open, c.peak, c.accepted
}

func (c *connectionCounts) dialLocal(address string) DialContextFunc {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.accepted++
		c.open++
		c.peak = max(c.peak, c.open)
		c.mu.Unlock()
		return &countedConn{Conn: conn, counts: c}, nil
	}
}

type countedConn struct {
	net.Conn
	counts *connectionCounts
	once   sync.Once
}

func (c *countedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		c.counts.mu.Lock()
		c.counts.open--
		c.counts.mu.Unlock()
	})
	return err
}

func localTLSServer(tb testing.TB, http2 bool, handler http.HandlerFunc) (*httptest.Server, *x509.CertPool) {
	tb.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = http2
	server.StartTLS()
	tb.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	return server, roots
}

func assertConnectionsClosed(tb testing.TB, counts *connectionCounts) {
	tb.Helper()
	if open, _, _ := counts.snapshot(); open != 0 {
		tb.Fatalf("%d client TCP connections remain after the candidate completed", open)
	}
}

func TestProbeReusesRoundsAndClosesEachCandidate(t *testing.T) {
	for _, http2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("HTTP2=%v", http2), func(t *testing.T) {
			var requests atomic.Int64
			server, roots := localTLSServer(t, http2, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if (r.ProtoMajor == 2) != http2 {
					t.Errorf("unexpected HTTP version %s", r.Proto)
				}
				_, _ = io.WriteString(w, goodTrace)
			})
			counts := &connectionCounts{}
			p := NewProber(Config{Timeout: time.Second, SNI: "example.com", HostHeader: "example.com", Rounds: 4, SkipFirst: 1, DialContext: counts.dialLocal(server.Listener.Addr().String())})
			p.client.Transport.(*http.Transport).TLSClientConfig.RootCAs = roots
			defer p.Close()
			for i := 1; i <= 12; i++ {
				ip := netip.AddrFrom4([4]byte{192, 0, 2, byte(i)})
				result := p.ProbeHTTPTraceMulti(context.Background(), ip)
				if !result.OK || result.Requests != 4 {
					t.Fatalf("candidate %d: %+v", i, result)
				}
				assertConnectionsClosed(t, counts)
				_, peak, accepted := counts.snapshot()
				if accepted != i || peak != 1 {
					t.Fatalf("rounds stopped reusing their connection: accepted=%d peak=%d candidates=%d", accepted, peak, i)
				}
			}
			if requests.Load() != 12*4 {
				t.Fatalf("requests=%d", requests.Load())
			}
		})
	}
}

func TestProbeClosesConnectionsOnFailureAndCancellation(t *testing.T) {
	for _, http2 := range []bool{false, true} {
		for _, failure := range []string{"status", "invalid trace", "redirect", "cancel", "timeout"} {
			t.Run(fmt.Sprintf("HTTP2=%v/%s", http2, failure), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var requests atomic.Int64
				server, roots := localTLSServer(t, http2, func(w http.ResponseWriter, r *http.Request) {
					if requests.Add(1) == 1 {
						_, _ = io.WriteString(w, goodTrace)
						return
					}
					switch failure {
					case "status":
						http.Error(w, "failure", http.StatusServiceUnavailable)
					case "invalid trace":
						_, _ = io.WriteString(w, "not a valid trace")
					case "redirect":
						w.Header().Set("Location", "https://other.invalid/file")
						w.WriteHeader(http.StatusFound)
					case "cancel", "timeout":
						w.WriteHeader(200)
						w.(http.Flusher).Flush()
						if failure == "cancel" {
							cancel()
						}
						<-r.Context().Done()
					}
				})
				counts := &connectionCounts{}
				p := NewProber(Config{Timeout: 150 * time.Millisecond, SNI: "example.com", Rounds: 3, SkipFirst: 1, DialContext: counts.dialLocal(server.Listener.Addr().String())})
				p.client.Transport.(*http.Transport).TLSClientConfig.RootCAs = roots
				defer p.Close()
				result := p.ProbeHTTPTraceMulti(ctx, testIP)
				if result.OK || result.Requests != 2 {
					t.Fatalf("unexpected failed candidate result: %+v", result)
				}
				assertConnectionsClosed(t, counts)
				_, _, accepted := counts.snapshot()
				if accepted != 1 {
					t.Fatalf("rounds used %d connections", accepted)
				}
			})
		}
	}
}

func TestSingleProbeAlsoClosesItsConnection(t *testing.T) {
	server, roots := localTLSServer(t, true, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, goodTrace)
	})
	counts := &connectionCounts{}
	p := NewProber(Config{Timeout: time.Second, SNI: "example.com", DialContext: counts.dialLocal(server.Listener.Addr().String())})
	p.client.Transport.(*http.Transport).TLSClientConfig.RootCAs = roots
	defer p.Close()
	if result := p.ProbeHTTPTrace(context.Background(), testIP); !result.OK {
		t.Fatal(result.Error)
	}
	assertConnectionsClosed(t, counts)
}

func TestDownloadClosesConnectionsOnFailureAndCancellation(t *testing.T) {
	for _, http2 := range []bool{false, true} {
		for _, failure := range []string{"status", "redirect", "small sample", "cancel", "timeout"} {
			t.Run(fmt.Sprintf("HTTP2=%v/%s", http2, failure), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				server, roots := localTLSServer(t, http2, func(w http.ResponseWriter, r *http.Request) {
					switch failure {
					case "status":
						http.Error(w, "failure", http.StatusServiceUnavailable)
					case "redirect":
						w.Header().Set("Location", "https://other.invalid/file")
						w.WriteHeader(http.StatusFound)
					case "small sample":
						_, _ = io.WriteString(w, "x")
					case "cancel", "timeout":
						w.WriteHeader(200)
						w.(http.Flusher).Flush()
						if failure == "cancel" {
							cancel()
						}
						<-r.Context().Done()
					}
				})
				counts := &connectionCounts{}
				p := NewDownloadProber(DownloadConfig{URL: "https://example.com:8443/file?sig=a%2Fb", Timeout: 150 * time.Millisecond, MinBytes: 32, DialContext: counts.dialLocal(server.Listener.Addr().String())})
				p.client.Transport.(*http.Transport).TLSClientConfig.RootCAs = roots
				defer p.Close()
				result := p.Download(ctx, testIP)
				if result.OK || result.Error == "" {
					t.Fatalf("unexpected failed download result: %+v", result)
				}
				assertConnectionsClosed(t, counts)
				_, _, accepted := counts.snapshot()
				if accepted != 1 {
					t.Fatalf("download used %d connections", accepted)
				}
			})
		}
	}
}

func TestCandidateConnectionsCancelPendingDial(t *testing.T) {
	ctx, closeConnections := withCandidateConnections(context.Background())
	defer closeConnections()
	started, finished := make(chan struct{}), make(chan error, 1)
	dial := trackCandidateConnections(func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	go func() {
		// net/http intentionally detaches dialing from request cancellation.
		_, err := dial(context.WithoutCancel(ctx), "tcp", "192.0.2.1:443")
		finished <- err
	}()
	<-started
	closeConnections()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pending dial returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("candidate cleanup did not cancel the pending dial")
	}
}

func TestCandidateConnectionsCloseLateDialResult(t *testing.T) {
	ctx, closeConnections := withCandidateConnections(context.Background())
	defer closeConnections()
	client, server := net.Pipe()
	defer server.Close()
	defer client.Close()
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	dial := trackCandidateConnections(func(context.Context, string, string) (net.Conn, error) {
		close(started)
		<-release
		return client, nil // Simulate a custom dialer that ignores cancellation.
	})
	go func() {
		_, err := dial(context.WithoutCancel(ctx), "tcp", "192.0.2.1:443")
		finished <- err
	}()
	<-started
	closeConnections()
	close(release)
	select {
	case err := <-finished:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("late dial returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("late dial did not finish")
	}
	if _, err := server.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("late connection remains open: %v", err)
	}
}
