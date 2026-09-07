package main

import (
	"fmt"
	"net/url"
	"time"

	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/probe"
)

type downloadOptions struct {
	top      int
	bytes    int64
	minBytes int64
	timeout  time.Duration
	url      string
	mode     string
}

// prepareDownloadConfig resolves defaults and validates even when no download
// candidate is eventually found. It performs no network operations.
func prepareDownloadConfig(opts downloadOptions) (probe.DownloadConfig, error) {
	if opts.top < 0 {
		return probe.DownloadConfig{}, fmt.Errorf("--download-top must be >= 0")
	}
	if opts.mode != "all" && opts.mode != "sequential" {
		return probe.DownloadConfig{}, fmt.Errorf("--download-mode must be all or sequential")
	}
	if opts.timeout <= 0 {
		return probe.DownloadConfig{}, fmt.Errorf("--download-timeout must be > 0")
	}
	if opts.url != "" {
		if err := probe.ValidateDownloadURL(opts.url); err != nil {
			return probe.DownloadConfig{}, fmt.Errorf("invalid --download-url: %w", err)
		}
	}
	if opts.bytes == 0 && opts.url == "" {
		opts.bytes = 50_000_000
	}
	if opts.bytes < 0 || opts.minBytes < 1 || (opts.bytes > 0 && opts.bytes < opts.minBytes) {
		return probe.DownloadConfig{}, fmt.Errorf("--download-bytes must be non-negative; --download-min-bytes must be positive and must not exceed the effective download size")
	}
	return probe.DownloadConfig{
		URL: opts.url, Timeout: opts.timeout, Bytes: opts.bytes, MinBytes: opts.minBytes,
	}, nil
}

func validateProbeOptions(timeout time.Duration, rounds, skipFirst int, mode string) error {
	if timeout <= 0 {
		return fmt.Errorf("--timeout must be > 0")
	}
	if rounds <= 0 {
		return fmt.Errorf("--rounds must be > 0")
	}
	if skipFirst < 0 || skipFirst >= rounds {
		return fmt.Errorf("--skip-first must be >= 0 and less than --rounds")
	}
	if mode != "auto" && mode != "trace" && mode != "http" {
		return fmt.Errorf("--probe-mode must be auto, trace, or http")
	}
	return nil
}

func downloadDisplayURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(invalid URL)"
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	// Query parameters may contain a download token. They remain in the
	// actual request but are not needed to identify the endpoint in logs.
	return u.Host + path
}
