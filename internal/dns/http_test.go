package dns

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type observedBody struct {
	io.Reader
	closed bool
}

func (b *observedBody) Close() error { b.closed = true; return nil }

func TestRequestJSONRejectsInvalidResponsesAndClosesBody(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"redirect", `{}`, 302},
		{"bad status", `{"success":true}`, 500},
		{"empty", ``, 200},
		{"null", `null`, 200},
		{"array", `[]`, 200},
		{"truncated", `{"result":`, 200},
		{"trailing JSON", `{} {}`, 200},
		{"oversized", `{"body":"` + strings.Repeat("x", maxResponseBytes) + `"}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &observedBody{Reader: strings.NewReader(tc.body)}
			calls := 0
			client := newHTTPClient()
			client.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Body: body, Request: r,
					Header: http.Header{"Location": {"https://redirect.invalid/"}}}, nil
			})
			var result map[string]any
			err := requestJSON(context.Background(), client, http.MethodGet, "https://api.invalid/test", "test-token", nil, &result)
			if err == nil || !body.closed || calls != 1 {
				t.Fatalf("err=%v body closed=%v calls=%d", err, body.closed, calls)
			}
		})
	}
}

func TestRequestTimeoutIncludesResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client := newHTTPClient()
	if client.Timeout != 10*time.Second {
		t.Fatal("default request timeout changed")
	}
	client.Timeout = 40 * time.Millisecond
	start := time.Now()
	var result map[string]any
	err := requestJSON(context.Background(), client, http.MethodGet, server.URL, "test-token", nil, &result)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("stalled response body was not bounded: %v", err)
	}
}

func TestCanceledRequestDoesNotCallTransport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := newHTTPClient()
	client.Transport = testTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("canceled request issued an HTTP operation")
		return nil, nil
	})
	var result map[string]any
	if err := requestJSON(ctx, client, http.MethodGet, "https://api.invalid", "test-token", nil, &result); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation cause lost: %v", err)
	}
}

func TestNewProviderValidatesLocally(t *testing.T) {
	for _, name := range []string{"CF_API_TOKEN", "CF_ZONE_ID", "VERCEL_TOKEN", "VERCEL_TEAM_ID"} {
		t.Setenv(name, "")
	}
	for _, cfg := range []Config{
		{Provider: "unknown"},
		{Provider: "cloudflare", Zone: "zone"},
		{Provider: "cloudflare", Token: "token"},
		{Provider: "vercel", Zone: "example.com"},
		{Provider: "vercel", Token: "token"},
		{Provider: "vercel", Token: "token", Zone: "https://example.com"},
		{Provider: "cloudflare", Token: "token", Zone: "zone", Subdomain: "cf?x=1"},
	} {
		if _, err := NewProvider(cfg); err == nil {
			t.Fatalf("invalid DNS config accepted: %+v", cfg)
		}
	}
	t.Setenv("CF_API_TOKEN", "test-token")
	t.Setenv("CF_ZONE_ID", "test-zone")
	if _, err := NewProvider(Config{Provider: "cloudflare", Subdomain: "cf"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VERCEL_TOKEN", "test-token")
	t.Setenv("VERCEL_TEAM_ID", "test-team")
	if _, err := NewProvider(Config{Provider: "vercel", Zone: "example.com", Subdomain: "@"}); err != nil {
		t.Fatal(err)
	}
}
