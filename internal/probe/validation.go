package probe

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"
)

const maxProbeBodyBytes = 64 * 1024

// A redirect must not turn a measurement of one candidate into a measurement
// of a hostname resolved to another address (or downgrade HTTPS to HTTP).
func rejectRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

func requestError(ctx context.Context, err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return "canceled"
	}
	return err.Error()
}

// The trace IP is the client's public address, not the candidate server IP.
func validTrace(trace map[string]string) bool {
	if _, err := netip.ParseAddr(trace["ip"]); err != nil {
		return false
	}
	colo := trace["colo"]
	if len(colo) != 3 {
		return false
	}
	for _, ch := range colo {
		if ch < 'A' || ch > 'Z' {
			return false
		}
	}
	return true
}

func requiresTrace(cfg Config) bool {
	if cfg.Mode == "trace" {
		return true
	}
	if cfg.Mode == "http" {
		return false
	}
	path, _, _ := strings.Cut(cfg.Path, "?")
	return path == "/cdn-cgi/trace"
}
