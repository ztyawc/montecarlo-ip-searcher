package engine

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"net/netip"
	"sort"
	"testing"
	"time"

	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/bandit"
	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/probe"
)

const comparisonAddresses = 4096
const comparisonBudget = 512
const comparisonTop = 10
const comparisonSeeds = 24

type syntheticObservation struct {
	latencyMS int64
	success   bool
}

type searchQuality struct {
	topMean, failurePercent float64
}

func syntheticMix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

func comparisonWorld(scenario string) []syntheticObservation {
	world := make([]syntheticObservation, comparisonAddresses)
	for i := range world {
		h := syntheticMix(uint64(i) + 20260906)
		fastRegion := i/256 == 3 || i/256 == 11
		latency := int64(120 + (h>>8)%81)
		success := h%23 != 0
		switch scenario {
		case "clustered":
			if fastRegion {
				latency = int64(15 + (h>>8)%21)
			}
		case "scattered":
			if h%20 == 0 {
				latency = int64(15 + (h>>8)%21)
			}
		case "fast_but_unreliable":
			if fastRegion {
				latency, success = int64(15+(h>>8)%21), h%10 >= 7
			} else {
				latency = int64(70 + (h>>8)%41)
			}
		case "flat":
			latency = int64(100 + (h>>8)%6)
		default:
			panic("unknown synthetic scenario")
		}
		world[i] = syntheticObservation{latencyMS: latency, success: success}
	}
	return world
}

func qualityFromSamples(samples []syntheticObservation) (searchQuality, error) {
	latencies := make([]int64, 0, len(samples))
	failed := 0
	for _, sample := range samples {
		if sample.success {
			latencies = append(latencies, sample.latencyMS)
		} else {
			failed++
		}
	}
	if len(latencies) < comparisonTop {
		return searchQuality{}, fmt.Errorf("only %d successful samples", len(latencies))
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	var sum float64
	for _, latency := range latencies[:comparisonTop] {
		sum += float64(latency)
	}
	return searchQuality{topMean: sum / comparisonTop, failurePercent: 100 * float64(failed) / float64(len(samples))}, nil
}

func baselineQuality(world []syntheticObservation, seed int64, stratified bool) (searchQuality, error) {
	rng := rand.New(rand.NewSource(seed))
	var indices []int
	if stratified {
		// Equal allocation to each /24 stratum; each address is sampled at most once.
		strata := make([][]int, comparisonAddresses/256)
		for i := range strata {
			strata[i] = rng.Perm(256)
		}
		for i := 0; i < comparisonBudget; i++ {
			stratum := i % len(strata)
			indices = append(indices, stratum*256+strata[stratum][i/len(strata)])
		}
	} else {
		indices = rng.Perm(len(world))[:comparisonBudget]
	}
	samples := make([]syntheticObservation, len(indices))
	seen := make(map[int]bool, len(indices))
	for i, index := range indices {
		if seen[index] {
			return searchQuality{}, fmt.Errorf("baseline repeated address %d", index)
		}
		seen[index] = true
		samples[i] = world[index]
	}
	return qualityFromSamples(samples)
}

func mcisSyntheticQuality(world []syntheticObservation, seed int64) (searchQuality, error) {
	cfg := DefaultConfig()
	cfg.Budget, cfg.TopN, cfg.Concurrency, cfg.Seed = comparisonBudget, comparisonTop, 1, seed
	e := New(cfg, probe.Config{Timeout: time.Second})
	e.seed = seed
	e.tree = bandit.NewArmTree([]netip.Prefix{netip.MustParsePrefix("198.18.0.0/20")}, cfg.ToTreeConfig())
	headCfg := cfg.ToHeadManagerConfig(1000)
	headCfg.BaseSeed = seed
	e.headManager = bandit.NewHeadManager(headCfg)
	e.topN = NewTopNCollector(cfg.TopN)
	e.tasks = make(chan probeTask, 1)
	e.seenIPs = make(map[netip.Addr]struct{})
	e.nextIPs = make(map[netip.Prefix]netip.Addr)
	e.exhausted = make(map[netip.Prefix]bool)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	samples := make([]syntheticObservation, 0, comparisonBudget)
	for i := 0; i < comparisonBudget; i++ {
		if err := e.submitOneTask(ctx, i%cfg.Heads); err != nil {
			return searchQuality{}, err
		}
		task := <-e.tasks
		address := task.ip.As4()
		index := int(address[2])*256 + int(address[3])
		if address[0] != 198 || address[1] != 18 || index >= len(world) {
			return searchQuality{}, fmt.Errorf("sampled address outside synthetic world: %s", task.ip)
		}
		sample := world[index]
		samples = append(samples, sample)
		e.processOneResult(probeDone{task: task, result: probe.Result{
			IP: task.ip, OK: sample.success, TotalMS: sample.latencyMS, Requests: 1,
		}}, 1000)
		e.completed++
		if e.completed%int64(cfg.SplitInterval) == 0 {
			e.trySplit()
		}
	}
	if len(e.seenIPs) != comparisonBudget || e.submitted != comparisonBudget {
		return searchQuality{}, fmt.Errorf("budget mismatch: unique=%d submitted=%d", len(e.seenIPs), e.submitted)
	}
	quality, err := qualityFromSamples(samples)
	if err != nil {
		return quality, err
	}
	// The independent sort must agree with the production ranking collector.
	var collectedMean float64
	for _, result := range e.topN.Snapshot() {
		collectedMean += result.ScoreMS / comparisonTop
	}
	if math.Abs(collectedMean-quality.topMean) > 1e-9 {
		return searchQuality{}, fmt.Errorf("ranking disagrees with independent samples: %.3f vs %.3f", collectedMean, quality.topMean)
	}
	return quality, nil
}

// BenchmarkSyntheticSearchComparison uses real selection/update/splitting code
// with synchronous, deterministic feedback. It deliberately never starts a
// probe worker or opens a socket. It measures synthetic choice quality, not
// real-network speed or behavior under concurrent response ordering.
// Run with -run '^$' -bench '^BenchmarkSyntheticSearchComparison$' -benchtime=1x.
func BenchmarkSyntheticSearchComparison(b *testing.B) {
	for _, scenario := range []string{"clustered", "scattered", "fast_but_unreliable", "flat"} {
		world := comparisonWorld(scenario)
		optimal, err := qualityFromSamples(world)
		if err != nil {
			b.Fatal(err)
		}
		for _, method := range []string{"mcis", "uniform", "stratified"} {
			b.Run(scenario+"/"+method, func(b *testing.B) {
				outcomes := make([]searchQuality, comparisonSeeds)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					for n := range outcomes {
						seed := int64(n + 1)
						var err error
						if method == "mcis" {
							outcomes[n], err = mcisSyntheticQuality(world, seed)
						} else {
							outcomes[n], err = baselineQuality(world, seed, method == "stratified")
						}
						if err != nil {
							b.Fatalf("seed %d: %v", seed, err)
						}
					}
				}
				b.StopTimer()
				var mean, failure, variance float64
				for _, outcome := range outcomes {
					mean += outcome.topMean / comparisonSeeds
					failure += outcome.failurePercent / comparisonSeeds
				}
				for _, outcome := range outcomes {
					variance += math.Pow(outcome.topMean-mean, 2) / (comparisonSeeds - 1)
				}
				b.ReportMetric(mean, "top10_ms")
				b.ReportMetric(math.Sqrt(variance), "top10_sd")
				b.ReportMetric(mean-optimal.topMean, "regret_ms")
				b.ReportMetric(failure, "fail_pct")
				b.ReportMetric(comparisonBudget, "IPs/run")
				b.ReportMetric(comparisonSeeds, "seeds")
			})
		}
	}
}
