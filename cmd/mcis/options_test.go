package main

import (
	"testing"
	"time"
)

func TestPrepareDownloadKeepsURLAndLimits(t *testing.T) {
	for _, tc := range []struct {
		name, url       string
		bytes, minBytes int64
		wantBytes       int64
	}{
		{"default endpoint", "", 0, 1_000_000, 50_000_000},
		{"custom root unlimited", "https://download.example", 0, 1_000_000, 0},
		{"custom port and escaping", "https://download.example:8443/a%2Fb%3Fc%23d?token=x%2Fy&part=2", 0, 1_000_000, 0},
		{"custom explicit limit", "https://download.example/file", 2_000_000, 1_000_000, 2_000_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := prepareDownloadConfig(downloadOptions{top: 5, bytes: tc.bytes, minBytes: tc.minBytes, timeout: 17 * time.Second, url: tc.url, mode: "sequential"})
			if err != nil {
				t.Fatal(err)
			}
			if cfg.URL != tc.url || cfg.Bytes != tc.wantBytes || cfg.MinBytes != tc.minBytes || cfg.Timeout != 17*time.Second {
				t.Fatalf("URL or limits were changed: %+v", cfg)
			}
		})
	}
}

func TestDownloadDisplayOmitsQuery(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"https://download.example:8443/a%2Fb%3Fc%23d?token=secret", "download.example:8443/a%2Fb%3Fc%23d"},
		{"https://download.example?token=secret", "download.example/"},
	} {
		if got := downloadDisplayURL(tc.input); got != tc.want {
			t.Fatalf("got %q, want %q", got, tc.want)
		}
	}
}
