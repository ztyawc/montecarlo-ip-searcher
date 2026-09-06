package probe

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"time"
)

type DownloadConfig struct {
	// URL is the complete custom HTTPS source. When set, its hostname, port,
	// escaped path and query take precedence over SNI, HostName and Path.
	URL         string
	Timeout     time.Duration
	Bytes       int64
	MinBytes    int64 // Minimum usable sample; zero selects DefaultMinDownloadBytes.
	SNI         string
	HostName    string
	Path        string
	DialContext DialContextFunc
	// CustomURL indicates the user supplied a custom download URL.
	// When true, the Path is used as-is (no "?bytes=N" appended).
	CustomURL bool
}

const DefaultMinDownloadBytes int64 = 1_000_000

type DownloadResult struct {
	IP      netip.Addr `json:"ip"`
	OK      bool       `json:"ok"`
	Status  int        `json:"status"`
	Error   string     `json:"error,omitempty"`
	Bytes   int64      `json:"bytes"`
	TotalMS int64      `json:"total_ms"`
	Mbps    float64    `json:"mbps"`
	When    time.Time  `json:"when"`
}

type DownloadProber struct {
	cfg       DownloadConfig
	client    *http.Client
	sourceURL *url.URL
	configErr error
}

func NewDownloadProber(cfg DownloadConfig) *DownloadProber {
	var sourceURL *url.URL
	var configErr error
	if cfg.URL != "" {
		cfg.CustomURL = true
		sourceURL, configErr = parseDownloadURL(cfg.URL)
		if configErr == nil {
			cfg.SNI = sourceURL.Hostname()
			cfg.HostName = sourceURL.Host
		}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 45 * time.Second
	}
	if cfg.MinBytes == 0 {
		cfg.MinBytes = DefaultMinDownloadBytes
	}
	// Default endpoint needs ?bytes=N in URL; custom URL can use Bytes==0 for "no limit".
	if cfg.Bytes == 0 && !cfg.CustomURL {
		cfg.Bytes = 50_000_000
	}
	if cfg.SNI == "" {
		cfg.SNI = "speed.cloudflare.com"
	}
	if cfg.HostName == "" {
		cfg.HostName = "speed.cloudflare.com"
	}
	if cfg.Path == "" {
		cfg.Path = "/__down"
	}
	if sourceURL == nil && configErr == nil {
		// Preserve the original host/path configuration for existing callers.
		sourceURL, configErr = url.Parse("https://" + cfg.HostName + cfg.Path)
		if configErr == nil && !cfg.CustomURL {
			if sourceURL.RawQuery != "" {
				sourceURL.RawQuery += "&"
			}
			sourceURL.RawQuery += "bytes=" + strconv.FormatInt(cfg.Bytes, 10)
		}
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
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig: &tls.Config{
			ServerName: cfg.SNI,
		},
	}

	return &DownloadProber{
		cfg:       cfg,
		sourceURL: sourceURL,
		configErr: configErr,
		client: &http.Client{
			Transport:     transport,
			Timeout:       cfg.Timeout,
			CheckRedirect: rejectRedirect,
		},
	}
}

func (p *DownloadProber) Close() { p.client.CloseIdleConnections() }

func (p *DownloadProber) Download(ctx context.Context, ip netip.Addr) DownloadResult {
	ctx, closeConnections := withCandidateConnections(ctx)
	defer func() {
		closeConnections()
		p.Close()
	}()
	start := time.Now()
	out := DownloadResult{
		IP:   ip,
		When: start,
	}
	if p.configErr != nil {
		out.Error = "invalid_download_url: " + p.configErr.Error()
		return out
	}
	if p.cfg.Bytes < 0 || p.cfg.MinBytes < 1 || (p.cfg.Bytes > 0 && p.cfg.Bytes < p.cfg.MinBytes) {
		out.Error = "invalid_download_size"
		return out
	}

	host := ip.String()
	if ip.Is6() {
		host = "[" + host + "]"
	}

	if port := p.sourceURL.Port(); port != "" {
		host = net.JoinHostPort(ip.String(), port)
	}
	// Only replace the authority used for dialing. Copying the URL preserves
	// RawPath, RawQuery and ForceQuery, including signed URLs and an empty query.
	targetURL := *p.sourceURL
	targetURL.Host = host
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL.String(), nil)
	if err != nil {
		out.Error = err.Error()
		out.TotalMS = time.Since(start).Milliseconds()
		return out
	}
	req.Host = p.cfg.HostName
	req.Header.Set("User-Agent", "mcis/0.1")
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := p.client.Do(req)
	if err != nil {
		out.Error = requestError(ctx, err)
		out.TotalMS = time.Since(start).Milliseconds()
		return out
	}
	defer func() { _ = resp.Body.Close() }()

	out.Status = resp.StatusCode
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		out.Error = fmt.Sprintf("redirect_%d", resp.StatusCode)
		out.TotalMS = time.Since(start).Milliseconds()
		return out
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		out.Error = fmt.Sprintf("http_status_%d", resp.StatusCode)
		out.TotalMS = time.Since(start).Milliseconds()
		return out
	}

	var n int64
	if p.cfg.Bytes == 0 {
		// No limit: read until EOF (custom URL only).
		n, err = io.Copy(io.Discard, resp.Body)
	} else {
		// Read at most cfg.Bytes.
		n, err = io.CopyN(io.Discard, resp.Body, p.cfg.Bytes)
	}
	elapsed := time.Since(start)
	out.TotalMS = elapsed.Milliseconds()
	out.Bytes = n
	if err != nil && !errors.Is(err, io.EOF) {
		out.Error = requestError(ctx, err)
		return out
	}
	if ctx.Err() != nil {
		out.Error = requestError(ctx, ctx.Err())
		return out
	}
	// Reaching our explicit cap is intentional, but premature EOF before a
	// declared Content-Length or the default endpoint's requested size is not.
	capReached := p.cfg.Bytes > 0 && n == p.cfg.Bytes
	if (!capReached && resp.ContentLength > n) || (!p.cfg.CustomURL && n < p.cfg.Bytes) {
		out.Error = "incomplete_download"
		return out
	}
	if n < p.cfg.MinBytes {
		out.Error = fmt.Sprintf("insufficient_sample: got=%d min=%d", n, p.cfg.MinBytes)
		return out
	}

	if elapsed > 0 {
		out.Mbps = (float64(n) * 8) / elapsed.Seconds() / 1e6
	}
	out.OK = true
	return out
}
