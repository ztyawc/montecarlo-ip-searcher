package engine

import (
	"context"
	"errors"
	"fmt"
	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/bandit"
	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/probe"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"
)

func TestFiniteSearchDrainsWithoutRepeatingAddresses(t *testing.T) {
	for _, tc := range []struct {
		name         string
		cidrs        []string
		budget, want int
		exhausted    bool
	}{
		{"v4 singleton", []string{"192.0.2.1/32"}, 2000, 1, true},
		{"v6 singleton", []string{"2001:db8::1/128"}, 100, 1, true},
		{"v4 exhausted", []string{"192.0.2.0/30"}, 100, 4, true},
		{"v6 exhausted", []string{"2001:db8::/126"}, 100, 4, true},
		{"overlapping roots", []string{"192.0.2.0/30", "192.0.2.0/31", "192.0.2.1/32", "192.0.2.0/30"}, 100, 4, true},
		{"neighboring roots", []string{"192.0.2.0/30", "192.0.2.4/30"}, 100, 8, true},
		{"budget smaller", []string{"192.0.2.0/24"}, 10, 10, false},
		{"many collisions", []string{"192.0.2.0/24"}, 300, 256, true},
		{"last IPv6 addresses", []string{"ffff:ffff:ffff:ffff:ffff:ffff:ffff:fffc/126"}, 10, 4, true},
	} {
		for _, concurrency := range []int{1, 4, 16} {
			t.Run(fmt.Sprintf("%s/concurrency=%d", tc.name, concurrency), func(t *testing.T) {
				cfg := DefaultConfig()
				cfg.Budget = tc.budget
				cfg.Concurrency = concurrency
				cfg.Heads = 4
				cfg.Seed = 19
				var mu sync.Mutex
				calls := make(map[string]int)
				pc := probe.Config{Timeout: time.Second, Rounds: 1, DialContext: func(_ context.Context, _, addr string) (net.Conn, error) {
					mu.Lock()
					calls[addr]++
					mu.Unlock()
					return nil, errors.New("local simulated connection failure")
				}}
				e := New(cfg, pc)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				r, err := e.Run(ctx, Request{CIDRs: tc.cidrs, Probe: pc})
				if ctx.Err() != nil {
					t.Fatal("search hung until deadline")
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(r.Top) != 0 {
					t.Fatalf("failures leaked into Top: %v", r.Top)
				}
				if r.Stats.UniqueIPs != int64(tc.want) || r.Stats.Completed != int64(tc.want) || r.Stats.Failed != int64(tc.want) || r.Stats.Exhausted != tc.exhausted {
					t.Fatalf("wrong stats %+v", r.Stats)
				}
				mu.Lock()
				defer mu.Unlock()
				if len(calls) != tc.want {
					t.Fatalf("got %d unique calls want %d", len(calls), tc.want)
				}
				for address, count := range calls {
					if count != 1 {
						t.Errorf("duplicate probe %s: %d calls", address, count)
					}
				}
			})
		}
	}
}

func TestEngineCanRunAgainWithoutOldReservations(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Budget = 10
	cfg.Concurrency = 1
	pc := probe.Config{Timeout: time.Second, Rounds: 1, DialContext: func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("mock") }}
	e := New(cfg, pc)
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		r, err := e.Run(ctx, Request{CIDRs: []string{"192.0.2.1/32"}, Probe: pc})
		cancel()
		if err != nil || r.Stats.UniqueIPs != 1 || r.Stats.Completed != 1 {
			t.Fatalf("run %d: %+v err=%v", i, r, err)
		}
	}
}

func TestSamplerFallbackFindsLastUnusedIP(t *testing.T) {
	prefix := netip.MustParsePrefix("192.0.2.0/24")
	e := &Engine{seenIPs: make(map[netip.Addr]struct{}), nextIPs: make(map[netip.Prefix]netip.Addr), exhausted: make(map[netip.Prefix]bool)}
	last := netip.MustParseAddr("192.0.2.255")
	for ip := prefix.Addr(); ip != last; ip = ip.Next() {
		e.seenIPs[ip] = struct{}{}
	}
	head := bandit.NewSearchHead(0, 19, 3000, 32)
	ip, err := e.sampleIPWithDedup(context.Background(), prefix, head)
	if err != nil || ip != last {
		t.Fatalf("got %s %v", ip, err)
	}
	ip, err = e.sampleIPWithDedup(context.Background(), prefix, head)
	if !errors.Is(err, errPrefixExhausted) || ip.IsValid() {
		t.Fatalf("got %s %v", ip, err)
	}
}

func TestSamplerCancellation(t *testing.T) {
	e := &Engine{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := e.sampleIPWithDedup(ctx, netip.MustParsePrefix("192.0.2.0/24"), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestOnlySuccessfulColoMatchesEnterTop(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ColoAllow = []string{"HKG"}
	e := New(cfg, probe.Config{})
	prefix := netip.MustParsePrefix("192.0.2.0/24")
	e.tree = bandit.NewArmTree([]netip.Prefix{prefix}, cfg.ToTreeConfig())
	e.topN = NewTopNCollector(10)
	for i, rc := range []struct {
		ok   bool
		colo string
	}{{false, "HKG"}, {true, "SJC"}, {true, "HKG"}} {
		ip := netip.MustParseAddr(fmt.Sprintf("192.0.2.%d", i+1))
		e.processOneResult(probeDone{task: probeTask{ip: ip, prefix: prefix}, result: probe.Result{IP: ip, OK: rc.ok, TotalMS: 10, Trace: map[string]string{"colo": rc.colo}}}, 3000)
	}
	results := e.topN.Snapshot()
	if len(results) != 1 || results[0].IP.String() != "192.0.2.3" {
		t.Fatalf("got %v", results)
	}
	stats := e.tree.GetNode(prefix).Stats()
	if stats.Failures != 1 || stats.Successes != 2 || e.failed != 1 || e.successful != 2 {
		t.Fatalf("lost diagnostic statistics %+v", stats)
	}
}

func TestTopCollectorDoesNotKeepLuckyMinimum(t *testing.T) {
	c := NewTopNCollector(2)
	ip := netip.MustParseAddr("192.0.2.1")
	c.Consider(TopResult{IP: ip, OK: false, ScoreMS: 1})
	if c.Len() != 0 {
		t.Fatal("failed candidate accepted")
	}
	c.Consider(TopResult{IP: ip, OK: true, ScoreMS: 10})
	c.Consider(TopResult{IP: ip, OK: true, ScoreMS: 90})
	if got := c.Snapshot(); len(got) != 1 || got[0].ScoreMS != 90 {
		t.Fatalf("stale minimum retained: %v", got)
	}
	c.Consider(TopResult{IP: ip, OK: false, ScoreMS: 6000})
	if c.Len() != 0 {
		t.Fatal("failed recheck did not invalidate candidate")
	}
}
