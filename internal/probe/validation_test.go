package probe

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type errorBody struct{ err error }

func (b errorBody) Read([]byte) (int, error) { return 0, b.err }
func (errorBody) Close() error               { return nil }

const goodTrace = "ip=198.51.100.9\ncolo=HKG\nh=example.com\n"

var testIP = netip.MustParseAddr("192.0.2.1")

func TestProbeValidation(t *testing.T) {
	for _, tc := range []struct {
		name, path, mode, body string
		status                 int
		bodyErr                error
		wantOK                 bool
		wantError              string
	}{
		{name: "valid trace", body: goodTrace, status: 200, wantOK: true},
		{name: "query remains trace", path: "/cdn-cgi/trace?x=1", body: goodTrace, status: 200, wantOK: true},
		{name: "empty trace", status: 200, wantError: "invalid_trace"},
		{name: "HTML is not trace", body: "<html>error</html>", status: 200, wantError: "invalid_trace"},
		{name: "missing colo", body: "ip=198.51.100.9\n", status: 200, wantError: "invalid_trace"},
		{name: "invalid IP", body: "ip=not-an-ip\ncolo=HKG\n", status: 200, wantError: "invalid_trace"},
		{name: "invalid colo", body: "ip=198.51.100.9\ncolo=HKG!\n", status: 200, wantError: "invalid_trace"},
		{name: "generic custom path", path: "/health", body: "healthy", status: 200, wantOK: true},
		{name: "generic 204", path: "/health", status: 204, wantOK: true},
		{name: "explicit generic", mode: "http", body: "healthy", status: 200, wantOK: true},
		{name: "explicit trace on custom path", mode: "trace", path: "/health", body: "healthy", status: 200, wantError: "invalid_trace"},
		{name: "invalid mode", mode: "invalid", status: 200, wantError: "invalid_probe_mode"},
		{name: "error status", body: goodTrace, status: 500, wantError: "http_status_500"},
		{name: "broken body", status: 200, bodyErr: io.ErrUnexpectedEOF, wantError: "unexpected EOF"},
		{name: "body timeout", status: 200, bodyErr: context.DeadlineExceeded, wantError: "timeout"},
		{name: "body canceled", status: 200, bodyErr: context.Canceled, wantError: "canceled"},
		{name: "oversize", body: goodTrace + strings.Repeat("x", maxProbeBodyBytes), status: 200, wantError: "response_too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProber(Config{Path: tc.path, Mode: tc.mode, Timeout: time.Second})
			t.Cleanup(p.Close)
			p.client.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Accept-Encoding") != "identity" {
					t.Error("probe must request identity encoding")
				}
				var body io.ReadCloser = io.NopCloser(strings.NewReader(tc.body))
				if tc.bodyErr != nil {
					body = errorBody{tc.bodyErr}
				}
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: body, Request: r}, nil
			})
			r := p.ProbeHTTPTrace(context.Background(), testIP)
			if r.OK != tc.wantOK || r.Error != tc.wantError {
				t.Fatalf("got OK=%v error=%q, want %v %q", r.OK, r.Error, tc.wantOK, tc.wantError)
			}
			if r.IP != testIP {
				t.Fatal("candidate attribution changed")
			}
		})
	}
}

func TestBothProbersRejectRedirects(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, location := range []string{"https://other.example/file", "http://other.example/file", "/relative"} {
			for _, download := range []bool{false, true} {
				calls := 0
				transport := testTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: status, Header: http.Header{"Location": {location}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				})
				var ok bool
				var probeError string
				if download {
					p := NewDownloadProber(DownloadConfig{})
					p.client.Transport = transport
					r := p.Download(context.Background(), testIP)
					ok, probeError = r.OK, r.Error
					p.Close()
				} else {
					p := NewProber(Config{})
					p.client.Transport = transport
					r := p.ProbeHTTPTrace(context.Background(), testIP)
					ok, probeError = r.OK, r.Error
					p.Close()
				}
				if ok || calls != 1 || !strings.HasPrefix(probeError, "redirect_") {
					t.Fatalf("status=%d location=%s download=%v calls=%d ok=%v error=%s", status, location, download, calls, ok, probeError)
				}
			}
		}
	}
}

func TestDownloadSampleValidation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		custom    bool
		cap, min  int64
		body      string
		length    int64
		bodyErr   error
		wantOK    bool
		wantError string
	}{
		{name: "full default", cap: 128, min: 64, body: strings.Repeat("x", 128), length: 128, wantOK: true},
		{name: "empty default", cap: 128, min: 64, length: 0, wantError: "incomplete_download"},
		{name: "short default", cap: 128, min: 64, body: strings.Repeat("x", 80), length: 80, wantError: "incomplete_download"},
		{name: "custom complete below cap", custom: true, cap: 128, min: 64, body: strings.Repeat("x", 80), length: 80, wantOK: true},
		{name: "custom sample too small", custom: true, cap: 128, min: 64, body: "error page", length: 10, wantError: "insufficient_sample"},
		{name: "custom empty", custom: true, cap: 0, min: 64, length: 0, wantError: "insufficient_sample"},
		{name: "custom unlimited", custom: true, cap: 0, min: 64, body: strings.Repeat("x", 80), length: 80, wantOK: true},
		{name: "declared length truncated", custom: true, cap: 256, min: 64, body: strings.Repeat("x", 80), length: 128, wantError: "incomplete_download"},
		{name: "intentional cap", custom: true, cap: 64, min: 64, body: strings.Repeat("x", 128), length: 128, wantOK: true},
		{name: "read error", custom: true, cap: 128, min: 64, bodyErr: io.ErrUnexpectedEOF, wantError: "unexpected EOF"},
		{name: "read timeout", custom: true, cap: 128, min: 64, bodyErr: context.DeadlineExceeded, wantError: "timeout"},
		{name: "read canceled", custom: true, cap: 128, min: 64, bodyErr: context.Canceled, wantError: "canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewDownloadProber(DownloadConfig{CustomURL: tc.custom, Bytes: tc.cap, MinBytes: tc.min})
			t.Cleanup(p.Close)
			p.client.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Accept-Encoding") != "identity" {
					t.Error("download must request identity encoding")
				}
				var body io.ReadCloser = io.NopCloser(strings.NewReader(tc.body))
				if tc.bodyErr != nil {
					body = errorBody{tc.bodyErr}
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body, ContentLength: tc.length, Request: r}, nil
			})
			r := p.Download(context.Background(), testIP)
			if r.OK != tc.wantOK || (tc.wantError != "" && !strings.HasPrefix(r.Error, tc.wantError)) {
				t.Fatalf("got %+v, want OK=%v error=%s", r, tc.wantOK, tc.wantError)
			}
			if !r.OK && r.Mbps != 0 {
				t.Fatal("invalid sample must not publish a valid speed")
			}
			if r.OK && (r.Bytes < tc.min || r.Mbps <= 0) {
				t.Fatalf("bad valid sample %+v", r)
			}
		})
	}
}

func TestTransportRetainsTLSVerificationAndDisablesCompression(t *testing.T) {
	p := NewProber(Config{})
	defer p.Close()
	d := NewDownloadProber(DownloadConfig{})
	defer d.Close()
	for _, client := range []*http.Client{p.client, d.client} {
		tr := client.Transport.(*http.Transport)
		if !tr.DisableCompression || tr.TLSClientConfig.InsecureSkipVerify || tr.Proxy != nil {
			t.Fatal("transport safety settings changed")
		}
	}
}

func TestRealTLSProbeAndBodyTimeout(t *testing.T) {
	for _, stall := range []bool{false, true} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Host != "example.com" {
				t.Errorf("Host=%q", r.Host)
			}
			if stall {
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				return
			}
			_, _ = io.WriteString(w, goodTrace)
		}))
		p := NewProber(Config{SNI: "example.com", HostHeader: "example.com", Timeout: 100 * time.Millisecond, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		}})
		roots := x509.NewCertPool()
		roots.AddCert(srv.Certificate())
		p.client.Transport.(*http.Transport).TLSClientConfig.RootCAs = roots
		r := p.ProbeHTTPTrace(context.Background(), testIP)
		p.Close()
		srv.Close()
		if stall && (r.OK || r.Error != "timeout") {
			t.Fatalf("stalled body got %+v", r)
		}
		if !stall && !r.OK {
			t.Fatalf("valid TLS probe failed %+v", r)
		}
	}
}

func TestCanceledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := NewProber(Config{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return nil, ctx.Err() }})
	defer p.Close()
	r := p.ProbeHTTPTrace(ctx, testIP)
	if r.OK || r.Error != "canceled" {
		t.Fatalf("got %+v", r)
	}
}

func TestMultiRoundStillRejectsFailedRound(t *testing.T) {
	p := NewProber(Config{Rounds: 3, SkipFirst: 1})
	defer p.Close()
	calls := 0
	p.client.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("second round failure")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(goodTrace)), Header: make(http.Header), Request: r}, nil
	})
	r := p.ProbeHTTPTraceMulti(context.Background(), testIP)
	if r.OK || calls != 2 || r.Requests != 2 {
		t.Fatalf("got %+v calls=%d", r, calls)
	}
}

func TestMultiRoundCountsSkippedWarmupRequests(t *testing.T) {
	p := NewProber(Config{Rounds: 3, SkipFirst: 1})
	defer p.Close()
	calls := 0
	p.client.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(goodTrace)), Header: make(http.Header), Request: r}, nil
	})
	r := p.ProbeHTTPTraceMulti(context.Background(), testIP)
	if !r.OK || calls != 3 || r.Requests != 3 || !validTrace(r.Trace) {
		t.Fatalf("got %+v calls=%d", r, calls)
	}
}

func TestInvalidDownloadConfigurationDoesNotDial(t *testing.T) {
	for _, cfg := range []DownloadConfig{{Bytes: -1}, {CustomURL: true, Bytes: -1}, {MinBytes: -1}, {Bytes: 100, MinBytes: 101}} {
		p := NewDownloadProber(cfg)
		p.client.Transport = testTransport(func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid configuration issued a request")
			return nil, nil
		})
		r := p.Download(context.Background(), testIP)
		p.Close()
		if r.OK || r.Error != "invalid_download_size" {
			t.Fatalf("cfg=%+v result=%+v", cfg, r)
		}
	}
}
