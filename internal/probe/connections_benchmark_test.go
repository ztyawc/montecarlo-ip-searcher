package probe

import (
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Each operation is a batch of 32 candidate IPs, with six successful rounds per
// IP. Both policies use the same local TLS server and request/averaging logic.
// The baseline reproduces the old policy: keep every candidate's connection in
// the worker's transport until batch cleanup. HTTP attempts never leave localhost.
//
// ns/op, B/op and allocs/op are per batch. FD, heap and RSS deltas measure the
// entire test process (client AND local server), after GC, outside the timer.
// connections/candidate and retained-conns count client TCP connections only.
func BenchmarkProbeConnectionLifetime(b *testing.B) {
	const candidates, rounds = 32, 6
	for _, http2 := range []bool{false, true} {
		for _, retain := range []bool{true, false} {
			policy := "release_after_candidate"
			if retain {
				policy = "retain_until_batch_end"
			}
			b.Run(fmt.Sprintf("HTTP2=%v/%s", http2, policy), func(b *testing.B) {
				b.StopTimer()
				var serverOpen, requests atomic.Int64
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if (r.ProtoMajor == 2) != http2 {
						b.Errorf("unexpected HTTP version %s", r.Proto)
					}
					requests.Add(1)
					_, _ = io.WriteString(w, goodTrace)
				}))
				server.EnableHTTP2 = http2
				server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
					switch state {
					case http.StateNew:
						serverOpen.Add(1)
					case http.StateClosed:
						serverOpen.Add(-1)
					}
				}
				server.StartTLS()
				defer server.Close()
				roots := x509.NewCertPool()
				roots.AddCert(server.Certificate())
				var totalConnections, totalPeak, totalRetained int
				var totalHeapKiB, totalRSSKiB, totalFD float64
				fdSupported, rssSupported := false, false
				b.ReportAllocs()
				for n := 0; n < b.N; n++ {
					counts := &connectionCounts{}
					dial := counts.dialLocal(server.Listener.Addr().String())
					p := NewProber(Config{Timeout: 5 * time.Second, SNI: "example.com", HostHeader: "example.com", Rounds: rounds, SkipFirst: 1, DialContext: dial})
					tr := p.client.Transport.(*http.Transport)
					tr.TLSClientConfig.RootCAs = roots
					if retain {
						// Before the change the transport called the dialer directly.
						tr.DialContext = dial
					}
					before := readBenchmarkResources()
					b.StartTimer()
					for i := 1; i <= candidates; i++ {
						ip := netip.AddrFrom4([4]byte{192, 0, 2, byte(i)})
						var result Result
						if retain {
							result = probeRetainingIdleConnections(p, context.Background(), ip)
						} else {
							result = p.ProbeHTTPTraceMulti(context.Background(), ip)
						}
						if !result.OK || result.Requests != rounds {
							p.Close()
							b.Fatalf("candidate %d: %+v", i, result)
						}
					}
					b.StopTimer()
					open, peak, accepted := counts.snapshot()
					wantOpen := 0
					if retain {
						wantOpen = candidates
					}
					if open != wantOpen || accepted != candidates {
						p.Close()
						b.Fatalf("open=%d accepted=%d, want %d and %d", open, accepted, wantOpen, candidates)
					}
					waitForBenchmarkConnections(b, &serverOpen, int64(wantOpen))
					after := readBenchmarkResources()
					totalConnections += accepted
					totalPeak += peak
					totalRetained += open
					totalHeapKiB += float64(int64(after.heap)-int64(before.heap)) / 1024
					if before.fds >= 0 && after.fds >= 0 {
						fdSupported = true
						totalFD += float64(after.fds - before.fds)
					}
					if before.rssKiB >= 0 && after.rssKiB >= 0 {
						rssSupported = true
						totalRSSKiB += float64(after.rssKiB - before.rssKiB)
					}
					// Keep the transport alive until its retained memory is measured.
					runtime.KeepAlive(p)
					p.Close()
					assertConnectionsClosed(b, counts)
					waitForBenchmarkConnections(b, &serverOpen, 0)
				}
				if requests.Load() != int64(b.N*candidates*rounds) {
					b.Fatalf("requests=%d want=%d", requests.Load(), b.N*candidates*rounds)
				}
				b.ReportMetric(float64(totalConnections)/float64(b.N*candidates), "connections/candidate")
				b.ReportMetric(float64(totalPeak)/float64(b.N), "peak-conns")
				b.ReportMetric(float64(totalRetained)/float64(b.N), "retained-conns")
				b.ReportMetric(totalHeapKiB/float64(b.N), "process-heap-KiB")
				if fdSupported {
					b.ReportMetric(totalFD/float64(b.N), "process-FD-delta")
				}
				if rssSupported {
					b.ReportMetric(totalRSSKiB/float64(b.N), "process-RSS-KiB")
				}
				b.ReportMetric(100, "success-percent")
			})
		}
	}
}

// This is the pre-change multi-round loop with no per-candidate cleanup.
func probeRetainingIdleConnections(p *Prober, ctx context.Context, ip netip.Addr) Result {
	var results []Result
	requests := 0
	for i := 0; i < p.cfg.Rounds; i++ {
		r := p.probeOnce(ctx, ip)
		requests += r.Requests
		results = append(results, r)
		if !r.OK {
			r.Requests = requests
			return r
		}
	}
	result := calculateAverage(results[p.cfg.SkipFirst:], ip)
	result.Requests = requests
	return result
}

type benchmarkResources struct {
	heap   uint64
	rssKiB int64
	fds    int
}

func readBenchmarkResources() benchmarkResources {
	// Clear both generations of sync.Pool and return unused Go pages before
	// measuring retained memory. These collections are outside benchmark time.
	runtime.GC()
	debug.FreeOSMemory()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	out := benchmarkResources{heap: stats.HeapAlloc, rssKiB: -1, fds: -1}
	if entries, err := os.ReadDir("/proc/self/fd"); err == nil {
		out.fds = len(entries)
	}
	if status, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, line := range strings.Split(string(status), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				if fields := strings.Fields(line); len(fields) >= 2 {
					if value, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
						out.rssKiB = value
					}
				}
				break
			}
		}
	}
	return out
}

func waitForBenchmarkConnections(b *testing.B, open *atomic.Int64, want int64) {
	b.Helper()
	deadline := time.Now().Add(time.Second)
	for open.Load() != want {
		if time.Now().After(deadline) {
			b.Fatalf("server TCP connections=%d want=%d", open.Load(), want)
		}
		time.Sleep(time.Millisecond)
	}
}
