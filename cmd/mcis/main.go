package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/dns"
	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/engine"
	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/privatesocks"
	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/probe"
)

type repeatStringFlag []string

func (r *repeatStringFlag) String() string { return strings.Join(*r, ",") }
func (r *repeatStringFlag) Set(v string) error {
	*r = append(*r, v)
	return nil
}

// isValidDomain validates if a string is a valid domain name.
// Returns true if the domain is valid, false otherwise.
func isValidDomain(domain string) bool {
	if domain == "" {
		return false
	}

	// Maximum length check (253 characters)
	if len(domain) > 253 {
		return false
	}

	// Domain regex pattern:
	// - Each label: alphanumeric + hyphens, but not starting/ending with hyphen
	// - Labels separated by dots
	// - Must have at least one dot (to distinguish from plain hostnames)
	// - Total length up to 253 characters
	// - Each label up to 63 characters
	const domainPattern = `^([a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?$`

	re := regexp.MustCompile(domainPattern)
	if !re.MatchString(domain) {
		return false
	}

	// Check each label length (max 63 chars per label)
	labels := strings.Split(domain, ".")
	for _, label := range labels {
		if len(label) > 63 {
			return false
		}
	}

	return true
}

func main() {
	var (
		cidrs      repeatStringFlag
		cidrFile   string
		budget     int
		topN       int
		concur     int
		heads      int
		beam       int
		timeout    time.Duration
		host       string
		sni        string
		hostHdr    string
		path       string
		probeMode  string
		dlTop      int
		dlBytes    int64
		dlMinBytes int64
		dlTimeout  time.Duration
		dlURL      string
		dlMode     string
		outFmt     string
		outPath    string
		splitV4    int
		splitV6    int
		minSplit   int
		maxBitsV4  int
		maxBitsV6  int
		seed       int64
		verbose    bool

		// DNS upload flags
		dnsProvider    string
		dnsToken       string
		dnsZone        string
		dnsSubdomain   string
		dnsUploadCount int
		dnsTeamID      string
		dnsTimeout     time.Duration

		// New engine parameters
		diversityWeight float64
		splitInterval   int

		// Probe rounds configuration
		rounds    int
		skipFirst int

		// Optional private SOCKS proxy
		privateSOCKSConfigPath string
		privateSOCKSAddress    string
		privateSOCKSUsername   string
		privateSOCKSPassword   string
		privateSOCKSMethod     string
		privateSOCKSTimeout    time.Duration

		// Colo filter
		coloAllow   string
		coloExclude string
	)

	flag.Var(&cidrs, "cidr", "CIDR to search (repeatable). Example: 1.1.0.0/16 or 2606:4700::/32")
	flag.StringVar(&cidrFile, "cidr-file", "", "Path to a file containing CIDRs (one per line, # comment supported)")
	flag.IntVar(&budget, "budget", 2000, "Total probe budget (number of IPs to probe)")
	flag.IntVar(&topN, "top", 20, "Top N IPs to output")
	flag.IntVar(&concur, "concurrency", 200, "Probe concurrency")
	flag.IntVar(&heads, "heads", 4, "Number of search heads (diversification)")
	flag.IntVar(&beam, "beam", 32, "Beam width per head (kept candidate prefixes)")
	flag.DurationVar(&timeout, "timeout", 3*time.Second, "Per-probe timeout")
	flag.StringVar(&host, "host", "example.com", "Host name used for BOTH TLS SNI and HTTP Host header (recommended)")
	flag.StringVar(&sni, "sni", "", "TLS SNI server name (deprecated: use --host)")
	flag.StringVar(&hostHdr, "host-header", "", "HTTP Host header (deprecated: use --host)")
	flag.StringVar(&path, "path", "/cdn-cgi/trace", "HTTP path to request")
	flag.StringVar(&probeMode, "probe-mode", "auto", "Response validation: auto (trace endpoint only)|trace|http; response limit 64 KiB")
	flag.IntVar(&dlTop, "download-top", 5, "After search, run download speed test for top N IPs (0 to disable)")
	flag.Int64Var(&dlBytes, "download-bytes", 0, "Download test size in bytes; 0 = 50M for default endpoint, no limit for custom URL (default: 0)")
	flag.Int64Var(&dlMinBytes, "download-min-bytes", probe.DefaultMinDownloadBytes, "Minimum bytes for a valid download speed sample")
	flag.DurationVar(&dlTimeout, "download-timeout", 45*time.Second, "Per-IP download test timeout")
	flag.StringVar(&dlURL, "download-url", "", "Custom download test URL (e.g. https://myhost.com/path/to/file). Overrides default speed.cloudflare.com")
	flag.StringVar(&dlMode, "download-mode", "all", "Download test mode: 'all' (test top N) or 'sequential' (test sequentially until N successes)")
	flag.StringVar(&outFmt, "out", "jsonl", "Output format: jsonl|csv|text|debug")
	flag.StringVar(&outPath, "out-file", "", "Write output to file (default: stdout)")
	flag.IntVar(&splitV4, "split-step-v4", 2, "When splitting an IPv4 prefix, increase prefix bits by this step")
	flag.IntVar(&splitV6, "split-step-v6", 4, "When splitting an IPv6 prefix, increase prefix bits by this step")
	flag.IntVar(&minSplit, "min-samples-split", 5, "Minimum samples on a prefix before it can be split")
	flag.IntVar(&maxBitsV4, "max-bits-v4", 24, "Maximum IPv4 prefix bits to drill down to")
	flag.IntVar(&maxBitsV6, "max-bits-v6", 56, "Maximum IPv6 prefix bits to drill down to")
	flag.Int64Var(&seed, "seed", 0, "Random seed (0 = time-based)")
	flag.BoolVar(&verbose, "v", false, "Verbose progress to stderr")

	// DNS upload flags
	flag.StringVar(&dnsProvider, "dns-provider", "", "DNS provider for uploading results (cloudflare|vercel)")
	flag.StringVar(&dnsToken, "dns-token", "", "DNS provider API token (or use CF_API_TOKEN/VERCEL_TOKEN env)")
	flag.StringVar(&dnsZone, "dns-zone", "", "DNS zone ID (Cloudflare) or domain (Vercel) (or use CF_ZONE_ID env)")
	flag.StringVar(&dnsSubdomain, "dns-subdomain", "", "Subdomain to update (e.g., 'cf' for cf.example.com)")
	flag.IntVar(&dnsUploadCount, "dns-upload-count", 0, "Number of IPs to upload (default: same as --download-top)")
	flag.StringVar(&dnsTeamID, "dns-team-id", "", "Vercel Team ID (optional, or use VERCEL_TEAM_ID env)")
	flag.DurationVar(&dnsTimeout, "dns-timeout", dns.DefaultUploadTimeout, "Total DNS upload timeout, starting after results are saved")

	// New engine parameters
	flag.Float64Var(&diversityWeight, "diversity-weight", 0.3, "Weight for head diversity (0-1, higher = more exploration)")
	flag.IntVar(&splitInterval, "split-interval", 20, "Check for split opportunities every N samples")

	// Probe rounds configuration
	flag.IntVar(&rounds, "rounds", 6, "Number of probe rounds per IP (default: 6)")
	flag.IntVar(&skipFirst, "skip-first", 1, "Skip first N rounds when calculating average (default: 1, skips handshake overhead)")

	// Optional private SOCKS proxy. Credentials may also be provided through
	// MCIS_PRIVATE_SOCKS_USERNAME and MCIS_PRIVATE_SOCKS_PASSWORD.
	flag.StringVar(&privateSOCKSConfigPath, "private-socks-config", "", "Path to a cross-platform private SOCKS JSON config file")
	flag.StringVar(&privateSOCKSAddress, "private-socks", "", "Private SOCKS proxy address as host:port (optional)")
	flag.StringVar(&privateSOCKSUsername, "private-socks-username", "", "Private SOCKS username (or MCIS_PRIVATE_SOCKS_USERNAME)")
	flag.StringVar(&privateSOCKSPassword, "private-socks-password", "", "Private SOCKS password (prefer MCIS_PRIVATE_SOCKS_PASSWORD)")
	flag.StringVar(&privateSOCKSMethod, "private-socks-method", "0x80", "Private SOCKS authentication method: 0x80 or 0x82")
	flag.DurationVar(&privateSOCKSTimeout, "private-socks-timeout", 10*time.Second, "Private SOCKS TCP and handshake timeout")

	// Colo filter (CDN node filter by trace colo)
	flag.StringVar(&coloAllow, "colo", "", "Comma-separated colo whitelist; only these CDN nodes enter results (e.g. HKG,SJC)")
	flag.StringVar(&coloExclude, "colo-exclude", "", "Comma-separated colo blacklist; exclude these CDN nodes from results (e.g. LAX,DFW)")

	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "error: unexpected positional arguments; use --cidr or --cidr-file")
		os.Exit(1)
	}
	if err := validateProbeOptions(timeout, rounds, skipFirst, probeMode); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	dlCfg, err := prepareDownloadConfig(downloadOptions{top: dlTop, bytes: dlBytes, minBytes: dlMinBytes, timeout: dlTimeout, url: dlURL, mode: dlMode})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if err := validateOutputOptions(outputOptions{format: outFmt, path: outPath}); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	dnsPlan, err := prepareDNSUpload(dns.Config{
		Provider:    dnsProvider,
		Token:       dnsToken,
		Zone:        dnsZone,
		Subdomain:   dnsSubdomain,
		UploadCount: dnsUploadCount,
		TeamID:      dnsTeamID,
	}, dlTop, dnsTimeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	visitedFlags := make(map[string]bool)
	flag.Visit(func(flagValue *flag.Flag) {
		visitedFlags[flagValue.Name] = true
	})

	// Validate --host parameter
	if !isValidDomain(host) {
		fmt.Fprintf(os.Stderr, "error: --host must be a valid domain name, got: %s\n", host)
		os.Exit(1)
	}

	// Colo: at most one of allow vs exclude
	if coloAllow != "" && coloExclude != "" {
		fmt.Fprintln(os.Stderr, "error: cannot use both --colo and --colo-exclude; use only one")
		os.Exit(1)
	}

	if privateSOCKSConfigPath != "" {
		fileConfig, err := privatesocks.LoadConfigFile(privateSOCKSConfigPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		if !visitedFlags["private-socks"] {
			privateSOCKSAddress = fileConfig.Endpoint()
		}
		if !visitedFlags["private-socks-username"] {
			privateSOCKSUsername = fileConfig.Username
		}
		if !visitedFlags["private-socks-password"] {
			privateSOCKSPassword = fileConfig.Password
		}
		if !visitedFlags["private-socks-method"] && fileConfig.Method != "" {
			privateSOCKSMethod = fileConfig.Method
		}
		if !visitedFlags["private-socks-timeout"] && fileConfig.HandshakeTimeout != "" {
			privateSOCKSTimeout, err = fileConfig.ParseHandshakeTimeout()
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
		}
	}

	if privateSOCKSAddress == "" && !visitedFlags["private-socks"] {
		privateSOCKSAddress = os.Getenv("MCIS_PRIVATE_SOCKS")
	}
	if privateSOCKSUsername == "" && !visitedFlags["private-socks-username"] {
		privateSOCKSUsername = os.Getenv("MCIS_PRIVATE_SOCKS_USERNAME")
	}
	if privateSOCKSPassword == "" && !visitedFlags["private-socks-password"] {
		privateSOCKSPassword = os.Getenv("MCIS_PRIVATE_SOCKS_PASSWORD")
	}

	var privateSOCKSDialer *privatesocks.Dialer
	if privateSOCKSAddress != "" {
		if privateSOCKSTimeout <= 0 {
			fmt.Fprintln(os.Stderr, "error: --private-socks-timeout must be > 0")
			os.Exit(1)
		}
		method, err := privatesocks.ParseMethod(privateSOCKSMethod)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

		privateSOCKSConfig := privatesocks.Config{
			Address:          privateSOCKSAddress,
			Username:         privateSOCKSUsername,
			Password:         privateSOCKSPassword,
			Method:           method,
			HandshakeTimeout: privateSOCKSTimeout,
		}
		privateSOCKSDialer, err = privatesocks.New(privateSOCKSConfig)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "proxy: private SOCKS enabled address=%s method=0x%02x\n", privateSOCKSAddress, method)
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Unify host: by default use --host for both SNI and Host header.
	if sni == "" {
		sni = host
	}
	if hostHdr == "" {
		hostHdr = host
	}

	// Parse colo lists (comma-separated, trim spaces)
	parseColoList := func(s string) []string {
		if s == "" {
			return nil
		}
		parts := strings.Split(s, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	}

	// Build engine config
	cfg := engine.Config{
		Budget:          budget,
		TopN:            topN,
		Concurrency:     concur,
		Heads:           heads,
		Beam:            beam,
		SplitStepV4:     splitV4,
		SplitStepV6:     splitV6,
		MinSamplesSplit: minSplit,
		MaxBitsV4:       maxBitsV4,
		MaxBitsV6:       maxBitsV6,
		Seed:            seed,
		Verbose:         verbose,
		DiversityWeight: diversityWeight,
		SplitInterval:   splitInterval,
		ColoAllow:       parseColoList(coloAllow),
		ColoBlock:       parseColoList(coloExclude),
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	probeCfg := probe.Config{
		Timeout:    timeout,
		SNI:        sni,
		HostHeader: hostHdr,
		Path:       path,
		Mode:       probeMode,
		Rounds:     rounds,
		SkipFirst:  skipFirst,
	}
	if privateSOCKSDialer != nil {
		probeCfg.DialContext = privateSOCKSDialer.DialContext
	}
	dlCfg.DialContext = probeCfg.DialContext

	req := engine.Request{
		CIDRs:    []string(cidrs),
		CIDRFile: cidrFile,
		Probe:    probeCfg,
	}

	// Create and run engine
	eng := engine.New(cfg, probeCfg)
	res, err := eng.Run(ctx, req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	// Download speed test
	if dlTop > 0 {
		if dlTop > len(res.Top) {
			dlTop = len(res.Top)
		}
		dlp := probe.NewDownloadProber(dlCfg)
		defer dlp.Close()
		if verbose {
			if dlURL != "" {
				bytesDesc := fmt.Sprintf("max %d bytes", dlCfg.Bytes)
				if dlCfg.Bytes == 0 {
					bytesDesc = "full file (no limit)"
				}
				fmt.Fprintf(os.Stderr, "download: using custom URL %s (top %d IPs, %s)\n",
					downloadDisplayURL(dlURL), dlTop, bytesDesc)
			} else {
				fmt.Fprintf(os.Stderr, "download: using default speed.cloudflare.com/__down (top %d IPs, %d bytes)\n",
					dlTop, dlCfg.Bytes)
			}
		}
		// Download test with mode support
		var testCount, successCount int
		var maxTests int
		if dlMode == "sequential" {
			maxTests = len(res.Top) // Sequential mode: test until we have enough successes or run out of IPs
		} else {
			maxTests = dlTop // All mode: test exactly dlTop IPs
		}

		for i := 0; i < maxTests && successCount < dlTop; i++ {
			r := &res.Top[i]
			dctx, dcancel := context.WithTimeout(ctx, dlTimeout)
			dr := dlp.Download(dctx, r.IP)
			dcancel()
			r.DownloadOK = dr.OK
			r.DownloadBytes = dr.Bytes
			r.DownloadMS = dr.TotalMS
			r.DownloadMbps = dr.Mbps
			r.DownloadError = dr.Error
			testCount++
			if dr.OK {
				successCount++
			}
			if verbose {
				fmt.Fprintf(os.Stderr, "download: rank=%d ip=%s ok=%v mbps=%.2f ms=%d bytes=%d err=%s\n",
					i+1, r.IP.String(), dr.OK, dr.Mbps, dr.TotalMS, dr.Bytes, dr.Error)
			}
			// In sequential mode, stop when we have enough successes
			if dlMode == "sequential" && successCount >= dlTop {
				break
			}
		}
		if verbose && dlMode == "sequential" {
			fmt.Fprintf(os.Stderr, "download: mode=sequential tested=%d succeeded=%d target=%d\n",
				testCount, successCount, dlTop)
		}
	}

	if code := finishRun(ctx, res, outputOptions{format: outFmt, path: outPath}, dnsPlan, verbose, os.Stdout, os.Stderr, dns.Upload); code != 0 {
		os.Exit(code)
	}
}
