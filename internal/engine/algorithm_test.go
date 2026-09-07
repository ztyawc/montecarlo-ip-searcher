package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/bandit"
	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/probe"
)

func TestNewPreservesExplicitConfiguration(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DiversityWeight = 0
	cfg.Beam = 1
	cfg.Seed = -19
	e := New(cfg, probe.Config{})
	if !reflect.DeepEqual(e.cfg, cfg) {
		t.Fatalf("New changed explicit configuration: got %+v want %+v", e.cfg, cfg)
	}
	if err := e.cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := e.cfg.ToHeadManagerConfig(1000).DiversityWeight; got != 0 {
		t.Fatalf("explicit zero diversity became %v", got)
	}
	if got := New(Config{}, probe.Config{}).cfg; !reflect.DeepEqual(got, Config{}) {
		t.Fatalf("New silently supplied missing configuration: %+v", got)
	}
}

func TestInvalidConfigurationFailsBeforeInputLoading(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Config)
		want string
	}{
		{"zero budget", func(c *Config) { c.Budget = 0 }, "budget must be > 0"},
		{"negative budget", func(c *Config) { c.Budget = -1 }, "budget must be > 0"},
		{"zero top", func(c *Config) { c.TopN = 0 }, "topN must be > 0"},
		{"zero concurrency", func(c *Config) { c.Concurrency = 0 }, "concurrency must be > 0"},
		{"zero heads", func(c *Config) { c.Heads = 0 }, "heads must be > 0"},
		{"zero beam", func(c *Config) { c.Beam = 0 }, "beam must be > 0"},
		{"zero split samples", func(c *Config) { c.MinSamplesSplit = 0 }, "minSamplesSplit must be > 0"},
		{"zero split interval", func(c *Config) { c.SplitInterval = 0 }, "splitInterval must be > 0"},
		{"negative split interval", func(c *Config) { c.SplitInterval = -1 }, "splitInterval must be > 0"},
		{"zero IPv4 split", func(c *Config) { c.SplitStepV4 = 0 }, "splitStepV4 must be in"},
		{"oversized IPv6 split", func(c *Config) { c.SplitStepV6 = 17 }, "splitStepV6 must be in"},
		{"negative diversity", func(c *Config) { c.DiversityWeight = -1 }, "diversityWeight must be in"},
		{"NaN diversity", func(c *Config) { c.DiversityWeight = math.NaN() }, "diversityWeight must be in"},
		{"infinite diversity", func(c *Config) { c.DiversityWeight = math.Inf(1) }, "diversityWeight must be in"},
		{"negative infinite diversity", func(c *Config) { c.DiversityWeight = math.Inf(-1) }, "diversityWeight must be in"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tc.edit(&cfg)
			_, err := New(cfg, probe.Config{}).Run(context.Background(), Request{CIDRs: []string{"invalid CIDR"}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want configuration error containing %q", err, tc.want)
			}
		})
	}
}

// One worker and immediate simulated failures make the result order fixed.
// No socket is opened; real concurrent network completion order is not replayable.
func seededFailureRun(t *testing.T, seed int64, inputs []string) (Response, []string) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Budget, cfg.TopN, cfg.Concurrency, cfg.Beam = 96, 12, 1, 3
	cfg.Seed, cfg.MaxBitsV4, cfg.MinSamplesSplit, cfg.SplitInterval = seed, 28, 2, 4
	var sequence []string
	pc := probe.Config{Timeout: time.Second, Rounds: 1, DialContext: func(_ context.Context, _, address string) (net.Conn, error) {
		sequence = append(sequence, address)
		return nil, errors.New("synthetic failure; no socket opened")
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := New(cfg, pc).Run(ctx, Request{CIDRs: inputs, Probe: pc})
	if err != nil || ctx.Err() != nil {
		t.Fatalf("seeded run: err=%v context=%v", err, ctx.Err())
	}
	if result.Stats.Completed != int64(cfg.Budget) || result.Stats.Failed != int64(cfg.Budget) || len(sequence) != cfg.Budget {
		t.Fatalf("incomplete synthetic run: %+v, %d dial attempts", result.Stats, len(sequence))
	}
	return result, sequence
}

func TestFixedSeedReplaysWithReorderedRoots(t *testing.T) {
	inputs := []string{"198.18.2.0/24", "198.18.0.0/23", "198.18.2.128/25", "198.18.3.0/24"}
	reversed := []string{inputs[3], inputs[2], inputs[1], inputs[0]}
	for _, seed := range []int64{19, -23, -1 << 63, 1<<63 - 1} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			result, first := seededFailureRun(t, seed, inputs)
			replay, second := seededFailureRun(t, seed, reversed)
			if result.Stats.Seed != seed || replay.Stats.Seed != seed || !reflect.DeepEqual(first, second) {
				t.Fatalf("seed %d did not reproduce its probe sequence", seed)
			}
		})
	}
}

func TestTimeSeedIsRecordedAndCanReplay(t *testing.T) {
	inputs := []string{"198.18.0.0/23", "198.18.2.0/23"}
	before := time.Now().UnixNano()
	result, first := seededFailureRun(t, 0, inputs)
	after := time.Now().UnixNano()
	seed := result.Stats.Seed
	if seed == 0 || seed < before || seed > after {
		t.Fatalf("recorded seed %d is outside run interval [%d,%d]", seed, before, after)
	}
	_, replay := seededFailureRun(t, seed, inputs)
	if !reflect.DeepEqual(first, replay) {
		t.Fatal("recorded time seed does not reproduce the actual sequence")
	}
}

func TestExploitationPrefixesFollowRankingOrder(t *testing.T) {
	prefixes := []netip.Prefix{
		netip.MustParsePrefix("198.18.2.0/24"),
		netip.MustParsePrefix("198.18.0.0/24"),
		netip.MustParsePrefix("198.18.3.0/24"),
		netip.MustParsePrefix("198.18.1.0/24"),
	}
	e := &Engine{topN: NewTopNCollector(8)}
	for i, sample := range []struct {
		prefix int
		score  float64
	}{{3, 16}, {2, 14}, {1, 11}, {0, 10}, {1, 12}, {0, 10.5}} {
		address := prefixes[sample.prefix].Addr().As4()
		address[3] = byte(i + 1)
		e.topN.Consider(TopResult{
			IP:     netip.AddrFrom4(address),
			Prefix: prefixes[sample.prefix], OK: true, ScoreMS: sample.score,
		})
	}
	want := []netip.Prefix{prefixes[0], prefixes[0], prefixes[0], prefixes[1], prefixes[1], prefixes[1], prefixes[2]}
	for i := 0; i < 50; i++ {
		if got := e.getExploitationPrefixes(); !reflect.DeepEqual(got, want) {
			t.Fatalf("unstable exploitation order: got %v want %v", got, want)
		}
	}
}

type algorithmReplay struct {
	tasks []probeTask
	top   []TopResult
	nodes []bandit.ArmStats
}

// Exercise the production scheduler operations with fixed synthetic feedback,
// including successful observations, splitting and exploitation of old parents.
func syntheticAlgorithmRun(t *testing.T, prefixes []netip.Prefix) algorithmReplay {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Budget, cfg.TopN, cfg.Concurrency, cfg.Beam = 192, 12, 1, 4
	cfg.Seed, cfg.MaxBitsV4, cfg.MinSamplesSplit, cfg.SplitInterval = 37, 28, 2, 7
	e := New(cfg, probe.Config{})
	e.tree = bandit.NewArmTree(prefixes, cfg.ToTreeConfig())
	e.headManager = bandit.NewHeadManager(cfg.ToHeadManagerConfig(1000))
	e.topN = NewTopNCollector(cfg.TopN)
	e.tasks = make(chan probeTask, 1)
	e.seenIPs = make(map[netip.Addr]struct{})
	e.nextIPs = make(map[netip.Prefix]netip.Addr)
	e.exhausted = make(map[netip.Prefix]bool)
	var result algorithmReplay
	parentExploited := false
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < cfg.Budget; i++ {
		if err := e.submitOneTask(ctx, i%cfg.Heads); err != nil {
			t.Fatal(err)
		}
		task := <-e.tasks
		result.tasks = append(result.tasks, task)
		parentExploited = parentExploited || e.tree.GetNode(task.prefix).Stats().IsSplit
		address := task.ip.As4()
		index := int64(address[2])*256 + int64(address[3])
		e.processOneResult(probeDone{task: task, result: probe.Result{
			IP: task.ip, OK: index%7 != 0, TotalMS: 10 + (index*29+index/7)%91, Requests: 1,
		}}, 1000)
		e.completed++
		if e.completed%int64(cfg.SplitInterval) == 0 {
			e.trySplit()
		}
	}
	if len(e.seenIPs) != cfg.Budget || e.tree.Size() <= len(prefixes) || !parentExploited {
		t.Fatalf("replay did not cover required paths: unique=%d nodes=%d exploitation=%v", len(e.seenIPs), e.tree.Size(), parentExploited)
	}
	result.top = e.topN.Snapshot()
	for _, node := range e.tree.AllNodes() {
		result.nodes = append(result.nodes, node.Stats())
	}
	return result
}

func TestFixedFeedbackReplaysSplitsAndExploitation(t *testing.T) {
	prefixes := []netip.Prefix{netip.MustParsePrefix("198.18.0.0/23"), netip.MustParsePrefix("198.18.2.0/23")}
	first := syntheticAlgorithmRun(t, prefixes)
	for i := 0; i < 3; i++ {
		replay := syntheticAlgorithmRun(t, []netip.Prefix{prefixes[1], prefixes[0]})
		if !reflect.DeepEqual(first, replay) {
			t.Fatal("fixed seed and feedback did not reproduce selected addresses, ranking and posterior state")
		}
	}
}

func TestRequestTimeoutPreservesFractionalMilliseconds(t *testing.T) {
	request := Request{Probe: probe.Config{Timeout: 250 * time.Microsecond}}
	if got := request.TimeoutMS(); got != 0.25 {
		t.Fatalf("submillisecond timeout truncated to %v", got)
	}
}
