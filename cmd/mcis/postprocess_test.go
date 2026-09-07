package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"math"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/dns"
	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/engine"
)

func testDNSConfig() dns.Config {
	return dns.Config{Provider: "cloudflare", Token: "test-token", Zone: "0123456789abcdef0123456789abcdef", Subdomain: "cf"}
}

func TestPrepareDNSUpload(t *testing.T) {
	for _, name := range []string{"CF_API_TOKEN", "CF_ZONE_ID", "VERCEL_TOKEN", "VERCEL_TEAM_ID"} {
		t.Setenv(name, "")
	}
	for _, tc := range []struct {
		name       string
		change     func(*dns.Config, *int, *time.Duration)
		wantError  string
		wantCount  int
		wantAbsent bool
	}{
		{name: "default count", wantCount: 5},
		{name: "explicit count", change: func(c *dns.Config, _ *int, _ *time.Duration) { c.UploadCount = 2 }, wantCount: 2},
		{name: "disabled", change: func(c *dns.Config, n *int, d *time.Duration) { *c = dns.Config{}; *n = 0; *d = 0 }, wantAbsent: true},
		{name: "missing subdomain", change: func(c *dns.Config, _ *int, _ *time.Duration) { c.Subdomain = "" }, wantError: "--dns-subdomain"},
		{name: "blank subdomain", change: func(c *dns.Config, _ *int, _ *time.Duration) { c.Subdomain = "  " }, wantError: "--dns-subdomain"},
		{name: "download disabled", change: func(_ *dns.Config, n *int, _ *time.Duration) { *n = 0 }, wantError: "--download-top"},
		{name: "negative count", change: func(c *dns.Config, _ *int, _ *time.Duration) { c.UploadCount = -1 }, wantError: "--dns-upload-count"},
		{name: "zero timeout", change: func(_ *dns.Config, _ *int, d *time.Duration) { *d = 0 }, wantError: "--dns-timeout"},
		{name: "negative timeout", change: func(_ *dns.Config, _ *int, d *time.Duration) { *d = -time.Second }, wantError: "--dns-timeout"},
		{name: "unknown provider", change: func(c *dns.Config, _ *int, _ *time.Duration) { c.Provider = "unsupported" }, wantError: "unknown DNS provider"},
		{name: "missing token", change: func(c *dns.Config, _ *int, _ *time.Duration) { c.Token = "" }, wantError: "API token required"},
		{name: "missing zone", change: func(c *dns.Config, _ *int, _ *time.Duration) { c.Zone = "" }, wantError: "zone ID required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, count, timeout := testDNSConfig(), 5, time.Minute
			if tc.change != nil {
				tc.change(&cfg, &count, &timeout)
			}
			plan, err := prepareDNSUpload(cfg, count, timeout)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("plan=%v err=%v, want error containing %q", plan, err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantAbsent {
				if plan != nil {
					t.Fatal("disabled DNS produced an upload plan")
				}
				return
			}
			if plan == nil || plan.uploadCount != tc.wantCount || plan.timeout != timeout || plan.subdomain != cfg.Subdomain || plan.provider.Name() != cfg.Provider {
				t.Fatalf("wrong upload plan: %+v", plan)
			}
		})
	}
}

func downloadRow(ip string, ok, downloadOK bool, mbps float64) engine.TopResult {
	addr, _ := netip.ParseAddr(ip)
	return engine.TopResult{IP: addr, OK: ok, DownloadOK: downloadOK, DownloadMbps: mbps}
}

func TestSelectDNSCandidates(t *testing.T) {
	rows := []engine.TopResult{
		downloadRow("192.0.2.1", true, false, 0),
		downloadRow("192.0.2.2", true, false, 0),
		downloadRow("192.0.2.3", true, true, 20),
		downloadRow("192.0.2.4", true, true, 80),
	}
	for _, tc := range []struct {
		name  string
		rows  []engine.TopResult
		limit int
		want  []string
	}{
		{"sequential successes after initial ranks", rows, 2, []string{"192.0.2.4", "192.0.2.3"}},
		{"explicit limit", rows, 1, []string{"192.0.2.4"}},
		{"fewer candidates than limit", rows, 5, []string{"192.0.2.4", "192.0.2.3"}},
		{"stable speed ties", append(append([]engine.TopResult{}, rows...), downloadRow("192.0.2.5", true, true, 80)), 3, []string{"192.0.2.4", "192.0.2.5", "192.0.2.3"}},
		{"failed, untested, or invalid", []engine.TopResult{downloadRow("192.0.2.1", false, true, 100), downloadRow("192.0.2.2", true, false, 100), downloadRow("invalid", true, true, 100)}, 5, nil},
		{"zero limit", rows, 0, nil},
		{"empty results", nil, 5, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]engine.TopResult(nil), tc.rows...)
			got := selectDNSCandidates(tc.rows, tc.limit)
			var ips []string
			for _, candidate := range got {
				ips = append(ips, candidate.IP.String())
			}
			if !reflect.DeepEqual(ips, tc.want) {
				t.Fatalf("got %v want %v", ips, tc.want)
			}
			if !reflect.DeepEqual(tc.rows, before) {
				t.Fatal("candidate selection changed the scan result order")
			}
		})
	}
}

func testResponse() engine.Response {
	return engine.Response{
		Top: []engine.TopResult{{
			IP: netip.MustParseAddr("192.0.2.1"), Prefix: netip.MustParsePrefix("192.0.2.0/24"),
			OK: true, Status: 200, ConnectMS: 5, TLSMS: 10, TTFBMS: 20, TotalMS: 25, ScoreMS: 25,
			Trace: map[string]string{"colo": "HKG"}, DownloadOK: true, DownloadBytes: 1000000, DownloadMS: 100, DownloadMbps: 80,
			PrefixSamples: 3, PrefixOK: 3,
		}},
		Stats: engine.RunStats{Budget: 5, UniqueIPs: 1, Completed: 1, Successful: 1, RequestAttempts: 6},
	}
}

const wantJSONL = "{\"ip\":\"192.0.2.1\",\"prefix\":\"192.0.2.0/24\",\"ok\":true,\"status\":200,\"connect_ms\":5,\"tls_ms\":10,\"ttfb_ms\":20,\"total_ms\":25,\"score_ms\":25,\"trace\":{\"colo\":\"HKG\"},\"download_ok\":true,\"download_bytes\":1000000,\"download_ms\":100,\"download_mbps\":80,\"prefix_samples\":3,\"prefix_ok\":3,\"prefix_fail\":0}\n"

func TestWriteResultsCompatibility(t *testing.T) {
	for _, tc := range []struct{ format, want string }{
		{"jsonl", wantJSONL},
		{"csv", "rank,ip,prefix,ok,status,connect_ms,tls_ms,ttfb_ms,total_ms,score_ms,samples_prefix,ok_prefix,fail_prefix,download_ok,download_mbps,download_ms,download_bytes,download_error,colo\n1,192.0.2.1,192.0.2.0/24,true,200,5,10,20,25,25.00,3,3,0,true,80.00,100,1000000,,HKG\n"},
		{"text", "1\t192.0.2.1\t25.0ms\tok=true\tstatus=200\tprefix=192.0.2.0/24\tcolo=HKG\tdl_ok=true\tdl_mbps=80.00\tdl_ms=100\n"},
	} {
		t.Run(tc.format, func(t *testing.T) {
			var out bytes.Buffer
			if err := writeResults(&out, tc.format, testResponse()); err != nil {
				t.Fatal(err)
			}
			if out.String() != tc.want {
				t.Fatalf("got %q want %q", out.String(), tc.want)
			}
		})
	}
	t.Run("debug", func(t *testing.T) {
		var out bytes.Buffer
		want := testResponse()
		if err := writeResults(&out, "debug", want); err != nil {
			t.Fatal(err)
		}
		var got engine.Response
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) || !strings.HasPrefix(out.String(), "{\n  \"top\": [\n") {
			t.Fatalf("debug output changed: %s", out.String())
		}
	})
}

type errorWriteCloser struct {
	writeErr, syncErr, closeErr error
	closed, synced              bool
}

func (w *errorWriteCloser) Sync() error {
	w.synced = true
	return w.syncErr
}

func (w *errorWriteCloser) Write(p []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return len(p), nil
}

func (w *errorWriteCloser) Close() error {
	w.closed = true
	return w.closeErr
}

func TestWriteAndCloseResultsReportsErrors(t *testing.T) {
	writeErr, syncErr, closeErr := errors.New("write failed"), errors.New("sync failed"), errors.New("close failed")
	for _, tc := range []struct {
		name                        string
		writeErr, syncErr, closeErr error
	}{
		{"write error", writeErr, nil, nil},
		{"sync error", nil, syncErr, nil},
		{"close error", nil, nil, closeErr},
		{"write and close errors", writeErr, nil, closeErr},
		{"sync and close errors", nil, syncErr, closeErr},
		{"success", nil, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &errorWriteCloser{writeErr: tc.writeErr, syncErr: tc.syncErr, closeErr: tc.closeErr}
			err := writeAndCloseResults(w, "debug", testResponse())
			if !w.closed {
				t.Fatal("output was not closed")
			}
			if tc.writeErr == nil && !w.synced {
				t.Fatal("complete output was not flushed before closing")
			}
			for _, want := range []error{tc.writeErr, tc.syncErr, tc.closeErr} {
				if want != nil && !errors.Is(err, want) {
					t.Fatalf("got %v, missing %v", err, want)
				}
			}
			if tc.writeErr == nil && tc.syncErr == nil && tc.closeErr == nil && err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Only Name is used by the CLI; upload tests inject the operation itself.
// Embedding the interface keeps these tests independent of provider internals.
type namedProvider struct{ dns.Provider }

func (namedProvider) Name() string { return "test" }

func testDNSPlan() *dnsUploadPlan {
	return &dnsUploadPlan{provider: namedProvider{}, subdomain: "cf", uploadCount: 5, timeout: time.Second}
}

func unexpectedUpload(t *testing.T) dnsUploadFunc {
	t.Helper()
	return func(context.Context, dns.Provider, string, []netip.Addr, bool) error {
		t.Fatal("DNS upload should not have been called")
		return nil
	}
}

func TestFinishRunPreservesResultsOnDNSError(t *testing.T) {
	for _, mode := range []string{"failure", "timeout", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "results.jsonl")
			plan := testDNSPlan()
			plan.timeout = 10 * time.Millisecond
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			calls := 0
			upload := func(ctx context.Context, _ dns.Provider, subdomain string, ips []netip.Addr, _ bool) error {
				calls++
				data, err := os.ReadFile(path)
				if err != nil || string(data) != wantJSONL {
					t.Fatalf("results not fully saved before DNS: %q, %v", data, err)
				}
				if subdomain != "cf" || len(ips) != 1 || ips[0].String() != "192.0.2.1" {
					t.Fatalf("wrong upload: %q %v", subdomain, ips)
				}
				if mode == "timeout" {
					<-ctx.Done()
					return ctx.Err()
				}
				return errors.New("simulated DNS failure")
			}
			var stdout, stderr bytes.Buffer
			code := finishRun(ctx, testResponse(), outputOptions{format: "jsonl", path: path}, plan, true, &stdout, &stderr, upload)
			if code != 1 || !strings.Contains(stderr.String(), "dns upload error:") {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			wantCalls := 1
			if mode == "cancelled" {
				wantCalls = 0
			}
			if calls != wantCalls || stdout.Len() != 0 {
				t.Fatalf("calls=%d stdout=%q", calls, stdout.String())
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != wantJSONL {
				t.Fatalf("saved results lost after DNS failure: %q, %v", data, err)
			}
			if mode == "timeout" && !strings.Contains(stderr.String(), "context deadline exceeded") {
				t.Fatalf("timeout was not reported: %s", stderr.String())
			}
		})
	}
}

type observedWriter struct {
	bytes.Buffer
	lastWrite time.Time
}

func (w *observedWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	w.lastWrite = time.Now()
	return n, err
}

func TestFinishRunStartsDeadlineAfterOutput(t *testing.T) {
	plan := testDNSPlan()
	var stdout observedWriter
	var stderr bytes.Buffer
	called := false
	upload := func(ctx context.Context, _ dns.Provider, _ string, _ []netip.Addr, _ bool) error {
		called = true
		deadline, ok := ctx.Deadline()
		if !ok || deadline.Add(-plan.timeout).Before(stdout.lastWrite) {
			t.Fatal("DNS timeout started before output completed")
		}
		if stdout.String() != wantJSONL {
			t.Fatalf("incomplete stdout at upload: %q", stdout.String())
		}
		return nil
	}
	if code := finishRun(context.Background(), testResponse(), outputOptions{format: "jsonl"}, plan, true, &stdout, &stderr, upload); code != 0 || !called {
		t.Fatalf("code=%d called=%v stderr=%q", code, called, stderr.String())
	}
}

func TestFinishRunSkipsDNSWithoutCandidates(t *testing.T) {
	for _, res := range []engine.Response{{}, {Top: []engine.TopResult{downloadRow("192.0.2.1", true, false, 0)}}} {
		var stdout, stderr bytes.Buffer
		code := finishRun(context.Background(), res, outputOptions{format: "debug"}, testDNSPlan(), true, &stdout, &stderr, unexpectedUpload(t))
		if code != 0 || !strings.Contains(stderr.String(), "no successful download-tested IPs") {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
		if !json.Valid(stdout.Bytes()) {
			t.Fatalf("invalid empty-result output: %q", stdout.String())
		}
	}
}

func TestFinishRunOutputErrorsPreventDNS(t *testing.T) {
	t.Run("invalid format preserves old file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "results.jsonl")
		if err := os.WriteFile(path, []byte("previous results"), 0600); err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		code := finishRun(context.Background(), testResponse(), outputOptions{format: "invalid", path: path}, testDNSPlan(), false, io.Discard, &stderr, unexpectedUpload(t))
		data, err := os.ReadFile(path)
		if code != 1 || err != nil || string(data) != "previous results" || !strings.Contains(stderr.String(), "unknown -out:") {
			t.Fatalf("code=%d data=%q err=%v stderr=%q", code, data, err, stderr.String())
		}
	})
	t.Run("debug encoding failure", func(t *testing.T) {
		res := testResponse()
		res.Top[0].ScoreMS = math.NaN()
		var stdout, stderr bytes.Buffer
		code := finishRun(context.Background(), res, outputOptions{format: "debug"}, testDNSPlan(), false, &stdout, &stderr, unexpectedUpload(t))
		if code != 1 || !strings.Contains(stderr.String(), "unsupported value") {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
	})
	for _, format := range []string{"jsonl", "csv", "text", "debug"} {
		t.Run(format+" writer failure", func(t *testing.T) {
			writer := &errorWriteCloser{writeErr: errors.New("output unavailable")}
			var stderr bytes.Buffer
			code := finishRun(context.Background(), testResponse(), outputOptions{format: format}, testDNSPlan(), false, writer, &stderr, unexpectedUpload(t))
			if code != 1 || !strings.Contains(stderr.String(), "output unavailable") || writer.closed {
				t.Fatalf("code=%d stderr=%q closed stdout=%v", code, stderr.String(), writer.closed)
			}
		})
	}
}

func TestCLIRejectsConfigurationBeforeScan(t *testing.T) {
	if os.Getenv("MCIS_TEST_CLI_CHILD") == "1" {
		for i, arg := range os.Args {
			if arg == "--" {
				os.Args = append([]string{"mcis"}, os.Args[i+1:]...)
				break
			}
		}
		flag.CommandLine = flag.NewFlagSet("mcis", flag.ExitOnError)
		main()
		os.Exit(0)
	}
	for _, name := range []string{
		"CF_API_TOKEN", "CF_ZONE_ID", "VERCEL_TOKEN", "VERCEL_TEAM_ID",
		"MCIS_PRIVATE_SOCKS", "MCIS_PRIVATE_SOCKS_USERNAME", "MCIS_PRIVATE_SOCKS_PASSWORD",
	} {
		t.Setenv(name, "")
	}
	for _, tc := range []struct {
		name, want string
		args       []string
	}{
		{"output format", "unknown -out:", []string{"--out=invalid"}},
		{"DNS token", "API token required", []string{"--dns-provider=cloudflare", "--dns-subdomain=cf", "--dns-zone=0123456789abcdef0123456789abcdef"}},
		{"DNS timeout", "--dns-timeout must be > 0", []string{"--dns-provider=cloudflare", "--dns-subdomain=cf", "--dns-timeout=0s"}},
		{"negative budget", "budget must be > 0", []string{"--budget=-1"}},
		{"zero budget", "budget must be > 0", []string{"--budget=0"}},
		{"zero concurrency", "concurrency must be > 0", []string{"--concurrency=0"}},
		{"negative concurrency", "concurrency must be > 0", []string{"--concurrency=-1"}},
		{"zero top", "topN must be > 0", []string{"--top=0"}},
		{"zero heads", "heads must be > 0", []string{"--heads=0"}},
		{"zero beam", "beam must be > 0", []string{"--beam=0"}},
		{"zero split interval", "splitInterval must be > 0", []string{"--split-interval=0"}},
		{"negative diversity", "diversityWeight", []string{"--diversity-weight=-0.1"}},
		{"NaN diversity", "diversityWeight", []string{"--diversity-weight=NaN"}},
		{"infinite diversity", "diversityWeight", []string{"--diversity-weight=+Inf"}},
		{"zero probe timeout", "--timeout must be > 0", []string{"--timeout=0s"}},
		{"zero rounds", "--rounds must be > 0", []string{"--rounds=0"}},
		{"negative skipped rounds", "--skip-first", []string{"--skip-first=-1"}},
		{"all rounds skipped", "--skip-first", []string{"--skip-first=6"}},
		{"negative download count", "--download-top", []string{"--download-top=-1"}},
		{"unknown download mode", "--download-mode", []string{"--download-mode=typo"}},
		{"zero download timeout", "--download-timeout", []string{"--download-timeout=0s"}},
		{"negative download bytes", "--download-bytes", []string{"--download-bytes=-1"}},
		{"zero minimum sample", "--download-min-bytes", []string{"--download-min-bytes=0"}},
		{"default below minimum", "--download-min-bytes", []string{"--download-min-bytes=50000001"}},
		{"unsupported download scheme", "invalid --download-url", []string{"--download-url=http://download.example/file"}},
		{"missing download host", "invalid --download-url", []string{"--download-url=https:///file"}},
		{"invalid download escape", "invalid --download-url", []string{"--download-url=https://download.example/%zz"}},
		{"invalid download port", "invalid --download-url", []string{"--download-url=https://download.example:65536/file"}},
		{"empty download port", "invalid --download-url", []string{"--download-url=https://download.example:/file"}},
		{"download user info", "invalid --download-url", []string{"--download-url=https://user:secret@download.example/file"}},
		{"download fragment", "invalid --download-url", []string{"--download-url=https://download.example/file#part"}},
		{"disabled download still validates URL", "invalid --download-url", []string{"--download-top=0", "--download-url=http://download.example/file"}},
		{"zero proxy timeout", "--private-socks-timeout", []string{"--private-socks=proxy.example:1080", "--private-socks-timeout=0s"}},
		{"positional argument", "unexpected positional arguments", []string{"unused-argument"}},
		// Positive controls reach the missing-file sentinel instead of a
		// validation error. The engine separately tests preservation of zero.
		{"zero diversity allowed", "missing.cidrs", []string{"--diversity-weight=0"}},
		{"skip zero rounds allowed", "missing.cidrs", []string{"--rounds=1", "--skip-first=0"}},
		{"full custom URL allowed", "missing.cidrs", []string{"--download-url=https://download.example:8443/a%2Fb%3Fc%23d?token=x%2Fy"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "results.jsonl")
			if err := os.WriteFile(path, []byte("previous results"), 0600); err != nil {
				t.Fatal(err)
			}
			// A missing input file is a fail-closed sentinel: even if validation
			// regresses, this process cannot begin probing any address.
			args := []string{"-test.run=^TestCLIRejectsConfigurationBeforeScan$", "--", "--cidr-file=" + filepath.Join(dir, "missing.cidrs"), "--out-file=" + path}
			args = append(args, tc.args...)
			cmd := exec.Command(os.Args[0], args...)
			cmd.Env = append(os.Environ(), "MCIS_TEST_CLI_CHILD=1")
			out, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || !strings.Contains(string(out), tc.want) {
				t.Fatalf("err=%v output=%q, want early %q", err, out, tc.want)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "previous results" {
				t.Fatalf("early validation changed old output: %q, %v", data, err)
			}
		})
	}
}
