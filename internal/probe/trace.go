package probe

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Timeout     time.Duration
	SNI         string
	HostHeader  string
	Path        string
	Mode        string // auto (default): validate /cdn-cgi/trace; trace: always validate; http: generic 2xx
	Rounds      int    // 总测试次数，默认6
	SkipFirst   int    // 跳过前N次，默认1（跳过第1次握手）
	DialContext DialContextFunc
}

// DialContextFunc establishes a TCP connection for an HTTP transport.
type DialContextFunc func(ctx context.Context, network, address string) (net.Conn, error)

type Result struct {
	IP        netip.Addr        `json:"ip"`
	OK        bool              `json:"ok"`
	Status    int               `json:"status"`
	Error     string            `json:"error,omitempty"`
	ConnectMS int64             `json:"connect_ms"`
	TLSMS     int64             `json:"tls_ms"`
	TTFBMS    int64             `json:"ttfb_ms"`
	TotalMS   int64             `json:"total_ms"`
	Trace     map[string]string `json:"trace,omitempty"`
	When      time.Time         `json:"when"`
	Requests  int               `json:"requests"` // HTTP attempts, including failed connection attempts.
}

type Prober struct {
	cfg    Config
	client *http.Client
}

// NewProber creates a reusable prober. It connects directly unless a custom
// DialContext function is configured.
func NewProber(cfg Config) *Prober {
	if cfg.Path == "" {
		cfg.Path = "/cdn-cgi/trace"
	}
	if !strings.HasPrefix(cfg.Path, "/") {
		cfg.Path = "/" + cfg.Path
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 3 * time.Second
	}

	dialContext := cfg.DialContext
	if dialContext == nil {
		dialContext = (&net.Dialer{
			Timeout:   cfg.Timeout,
			KeepAlive: 30 * time.Second,
		}).DialContext
	}

	transport := &http.Transport{
		Proxy:                 nil, // critical: ignore HTTP(S)_PROXY and NO_PROXY env vars
		DisableCompression:    true,
		DialContext:           trackCandidateConnections(dialContext),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          1024,
		MaxIdleConnsPerHost:   256,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   cfg.Timeout,
		ResponseHeaderTimeout: cfg.Timeout,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig: &tls.Config{
			ServerName: cfg.SNI,
		},
	}
	client := &http.Client{
		Transport:     transport,
		Timeout:       cfg.Timeout,
		CheckRedirect: rejectRedirect,
	}

	return &Prober{cfg: cfg, client: client}
}

func (p *Prober) Close() { p.client.CloseIdleConnections() }

// probeOnce performs a single HTTP probe request.
func (p *Prober) probeOnce(ctx context.Context, ip netip.Addr) (res Result) {
	start := time.Now()
	res = Result{
		IP:   ip,
		When: start,
	}

	targetHost := ip.String()
	// URL host must wrap IPv6 in brackets.
	if ip.Is6() {
		targetHost = "[" + targetHost + "]"
	}

	url := "https://" + targetHost + p.cfg.Path

	var (
		timingMu     sync.Mutex
		connectStart time.Time
		tlsStart     time.Time
		gotFirstByte time.Time
		connectDur   time.Duration
		tlsDur       time.Duration
	)
	// Trace callbacks may finish after Do returns on cancellation.
	defer func() {
		timingMu.Lock()
		defer timingMu.Unlock()
		res.TotalMS = time.Since(start).Milliseconds()
		res.ConnectMS = connectDur.Milliseconds()
		res.TLSMS = tlsDur.Milliseconds()
		if !gotFirstByte.IsZero() {
			res.TTFBMS = gotFirstByte.Sub(start).Milliseconds()
		}
	}()

	trace := &httptrace.ClientTrace{
		ConnectStart: func(network, addr string) {
			timingMu.Lock()
			defer timingMu.Unlock()
			connectStart = time.Now()
		},
		ConnectDone: func(network, addr string, err error) {
			timingMu.Lock()
			defer timingMu.Unlock()
			if !connectStart.IsZero() {
				connectDur = time.Since(connectStart)
			}
		},
		TLSHandshakeStart: func() {
			timingMu.Lock()
			defer timingMu.Unlock()
			tlsStart = time.Now()
		},
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			timingMu.Lock()
			defer timingMu.Unlock()
			if !tlsStart.IsZero() {
				tlsDur = time.Since(tlsStart)
			}
		},
		GotFirstResponseByte: func() {
			timingMu.Lock()
			defer timingMu.Unlock()
			gotFirstByte = time.Now()
		},
	}

	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, url, nil)
	if err != nil {
		res.Error = err.Error()
		res.TotalMS = time.Since(start).Milliseconds()
		return res
	}
	if p.cfg.HostHeader != "" {
		req.Host = p.cfg.HostHeader
	}
	req.Header.Set("User-Agent", "mcis/0.1")
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("Accept-Encoding", "identity")
	if p.cfg.Mode != "" && p.cfg.Mode != "auto" && p.cfg.Mode != "trace" && p.cfg.Mode != "http" {
		res.Error = "invalid_probe_mode"
		return res
	}

	res.Requests = 1
	httpRes, err := p.client.Do(req)
	if err != nil {
		res.Error = requestError(ctx, err)
		return res
	}
	defer func() { _ = httpRes.Body.Close() }()

	res.Status = httpRes.StatusCode
	if httpRes.StatusCode >= 300 && httpRes.StatusCode < 400 {
		res.Error = fmt.Sprintf("redirect_%d", httpRes.StatusCode)
		return res
	}
	if httpRes.StatusCode < 200 || httpRes.StatusCode >= 300 {
		res.Error = fmt.Sprintf("http_status_%d", httpRes.StatusCode)
		return res
	}
	body, err := io.ReadAll(io.LimitReader(httpRes.Body, maxProbeBodyBytes+1))
	if err != nil {
		res.Error = requestError(ctx, err)
		return res
	}
	if ctx.Err() != nil {
		res.Error = requestError(ctx, ctx.Err())
		return res
	}
	if len(body) > maxProbeBodyBytes {
		res.Error = "response_too_large"
		return res
	}
	if httpRes.ContentLength > int64(len(body)) {
		res.Error = "incomplete_response"
		return res
	}
	res.Trace = parseTrace(string(body))
	if requiresTrace(p.cfg) && !validTrace(res.Trace) {
		res.Error = "invalid_trace"
		return res
	}
	res.OK = true
	return res
}

// ProbeHTTPTrace probes https://<ip>/<path> with SNI/HostHeader.
// This is a convenience wrapper that calls probeOnce for backward compatibility.
func (p *Prober) ProbeHTTPTrace(ctx context.Context, ip netip.Addr) Result {
	ctx, closeConnections := withCandidateConnections(ctx)
	defer func() {
		closeConnections()
		p.Close()
	}()
	return p.probeOnce(ctx, ip)
}

// ProbeHTTPTraceMulti performs multiple probes and returns the average of rounds after skipping the first N.
// This avoids the TCP/TLS handshake overhead in the first request and provides more stable latency measurements.
func (p *Prober) ProbeHTTPTraceMulti(ctx context.Context, ip netip.Addr) Result {
	// A worker never revisits a completed candidate. Reuse its connection across
	// the rounds, then release it on success, failure or cancellation.
	ctx, closeConnections := withCandidateConnections(ctx)
	defer func() {
		closeConnections()
		p.Close()
	}()
	rounds := p.cfg.Rounds
	if rounds <= 0 {
		rounds = 6
	}
	skipFirst := p.cfg.SkipFirst
	if skipFirst < 0 {
		skipFirst = 1
	}

	var results []Result
	requests := 0
	for i := 0; i < rounds; i++ {
		r := p.probeOnce(ctx, ip)
		requests += r.Requests
		results = append(results, r)
		// If any round fails, return the failure immediately
		if !r.OK {
			r.Requests = requests
			return r
		}
	}

	// Skip the first N rounds (which include handshake overhead) and calculate average
	if len(results) <= skipFirst {
		last := results[len(results)-1]
		last.Requests = requests
		return last
	}

	validResults := results[skipFirst:]
	result := calculateAverage(validResults, ip)
	result.Requests = requests
	return result
}

// calculateAverage computes the average of multiple probe results.
func calculateAverage(results []Result, ip netip.Addr) Result {
	if len(results) == 0 {
		return Result{IP: ip, OK: false, Error: "no valid results"}
	}

	avg := Result{
		IP:   ip,
		OK:   true,
		When: results[0].When,
	}

	var totalConnectMS, totalTLSMS, totalTTFBMS, totalTotalMS int64
	var validConnect, validTLS, validTTFB int

	for _, r := range results {
		totalTotalMS += r.TotalMS
		if r.ConnectMS > 0 {
			totalConnectMS += r.ConnectMS
			validConnect++
		}
		if r.TLSMS > 0 {
			totalTLSMS += r.TLSMS
			validTLS++
		}
		if r.TTFBMS > 0 {
			totalTTFBMS += r.TTFBMS
			validTTFB++
		}
		// Use the status and trace from the last successful result
		avg.Status = r.Status
		avg.Trace = r.Trace
	}

	count := int64(len(results))
	avg.TotalMS = totalTotalMS / count

	if validConnect > 0 {
		avg.ConnectMS = totalConnectMS / int64(validConnect)
	}
	if validTLS > 0 {
		avg.TLSMS = totalTLSMS / int64(validTLS)
	}
	if validTTFB > 0 {
		avg.TTFBMS = totalTTFBMS / int64(validTTFB)
	}

	return avg
}

func parseTrace(s string) map[string]string {
	m := make(map[string]string)
	lines := strings.Split(s, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k != "" {
			m[k] = v
		}
	}
	return m
}
