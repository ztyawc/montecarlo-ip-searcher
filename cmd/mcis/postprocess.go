package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/dns"
	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/engine"
	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/output"
)

type dnsUploadPlan struct {
	provider    dns.Provider
	subdomain   string
	uploadCount int
	timeout     time.Duration
}

// prepareDNSUpload validates local configuration without making DNS API calls.
// Resolve the default count before downloadTop is capped by the search results.
func prepareDNSUpload(cfg dns.Config, downloadTop int, timeout time.Duration) (*dnsUploadPlan, error) {
	if cfg.Provider == "" {
		return nil, nil
	}
	if strings.TrimSpace(cfg.Subdomain) == "" {
		return nil, fmt.Errorf("--dns-subdomain is required when --dns-provider is set")
	}
	if downloadTop <= 0 {
		return nil, fmt.Errorf("--download-top must be > 0 when using DNS upload")
	}
	if cfg.UploadCount < 0 {
		return nil, fmt.Errorf("--dns-upload-count must be >= 0")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("--dns-timeout must be > 0")
	}
	provider, err := dns.NewProvider(cfg)
	if err != nil {
		return nil, err
	}
	uploadCount := cfg.UploadCount
	if uploadCount == 0 {
		uploadCount = downloadTop
	}
	return &dnsUploadPlan{
		provider:    provider,
		subdomain:   cfg.Subdomain,
		uploadCount: uploadCount,
		timeout:     timeout,
	}, nil
}

type dnsCandidate struct {
	IP   netip.Addr
	Mbps float64
}

func selectDNSCandidates(rows []engine.TopResult, limit int) []dnsCandidate {
	if limit <= 0 {
		return nil
	}
	candidates := make([]dnsCandidate, 0, len(rows))
	// Sequential downloads can succeed beyond the first downloadTop rows.
	for _, r := range rows {
		if r.OK && r.DownloadOK && r.IP.IsValid() {
			candidates = append(candidates, dnsCandidate{IP: r.IP, Mbps: r.DownloadMbps})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Mbps > candidates[j].Mbps
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates
}

type outputOptions struct {
	format string
	path   string
}

func validateOutputFormat(format string) error {
	switch format {
	case "jsonl", "csv", "text", "debug":
		return nil
	default:
		return fmt.Errorf("unknown -out: %s", format)
	}
}

func validateOutputOptions(opts outputOptions) error {
	if err := validateOutputFormat(opts.format); err != nil {
		return err
	}
	if opts.path == "" {
		return nil
	}
	if _, err := resultFileMode(opts.path); err != nil {
		return err
	}
	parent, err := os.Stat(filepath.Dir(opts.path))
	if err != nil {
		return err
	}
	if !parent.IsDir() {
		return fmt.Errorf("output parent must be a directory: %s", filepath.Dir(opts.path))
	}
	return nil
}

func resultFileMode(path string) (os.FileMode, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0600, nil
	}
	if err != nil {
		return 0, err
	}
	// Replacing a link or device would change its meaning. Stream output to
	// stdout when a pipe or other special destination is needed.
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("output path must be a regular file: %s", path)
	}
	return info.Mode().Perm(), nil
}

func writeResults(w io.Writer, format string, res engine.Response) error {
	switch format {
	case "jsonl":
		return output.WriteJSONL(w, res.Top)
	case "csv":
		return output.WriteCSV(w, res.Top)
	case "text":
		return output.WriteText(w, res.Top)
	case "debug":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	default:
		return validateOutputFormat(format)
	}
}

type resultWriteCloser interface {
	io.WriteCloser
	Sync() error
}

func writeAndCloseResults(w resultWriteCloser, format string, res engine.Response) error {
	writeErr := writeResults(w, format, res)
	var syncErr error
	if writeErr == nil {
		syncErr = w.Sync()
	}
	closeErr := w.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}

func saveResults(opts outputOptions, res engine.Response, stdout io.Writer) (err error) {
	if err := validateOutputOptions(opts); err != nil {
		return err
	}
	if opts.path == "" {
		return writeResults(stdout, opts.format, res)
	}
	mode, err := resultFileMode(opts.path)
	if err != nil {
		return err
	}
	// A temporary file in the destination directory keeps the old output
	// intact until encoding, flushing and closing have all succeeded.
	f, err := os.CreateTemp(filepath.Dir(opts.path), ".mcis-results-*")
	if err != nil {
		return err
	}
	defer func() {
		if removeErr := os.Remove(f.Name()); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove temporary result file: %w", removeErr))
		}
	}()
	if err := f.Chmod(mode); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := writeAndCloseResults(f, opts.format, res); err != nil {
		return err
	}
	return os.Rename(f.Name(), opts.path)
}

type dnsUploadFunc func(context.Context, dns.Provider, string, []netip.Addr, bool) error

// finishRun saves the scan before attempting DNS changes. It keeps DNS errors
// on stderr and preserves the nonzero exit status without losing scan output.
func finishRun(ctx context.Context, res engine.Response, opts outputOptions, plan *dnsUploadPlan, verbose bool, stdout, stderr io.Writer, upload dnsUploadFunc) int {
	if err := saveResults(opts, res, stdout); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if plan == nil {
		return 0
	}
	candidates := selectDNSCandidates(res.Top, plan.uploadCount)
	if len(candidates) == 0 {
		if verbose {
			fmt.Fprintln(stderr, "dns: no successful download-tested IPs to upload")
		}
		return 0
	}
	if err := ctx.Err(); err != nil {
		fmt.Fprintln(stderr, "dns upload error:", err)
		return 1
	}
	ips := make([]netip.Addr, len(candidates))
	for i, candidate := range candidates {
		ips[i] = candidate.IP
	}
	if verbose {
		fmt.Fprintf(stderr, "dns: uploading %d IPs to %s (subdomain: %s), sorted by download speed...\n",
			len(ips), plan.provider.Name(), plan.subdomain)
		for i, candidate := range candidates {
			fmt.Fprintf(stderr, "  %d. %s (%.2f Mbps)\n", i+1, candidate.IP, candidate.Mbps)
		}
	}
	// Start the DNS deadline here; scanning, downloading, and saving consume none
	// of this budget, while cancellation of the overall command still applies.
	dctx, cancel := context.WithTimeout(ctx, plan.timeout)
	defer cancel()
	if err := upload(dctx, plan.provider, plan.subdomain, ips, verbose); err != nil {
		fmt.Fprintln(stderr, "dns upload error:", err)
		return 1
	}
	return 0
}
