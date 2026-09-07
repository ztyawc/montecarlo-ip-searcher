package engine

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/bandit"
	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/cidr"
	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/probe"
)

// Engine is the core search engine using hierarchical Thompson Sampling.
type Engine struct {
	cfg      Config
	probeCfg probe.Config

	tree        *bandit.ArmTree
	headManager *bandit.HeadManager
	topN        *TopNCollector

	// Worker coordination
	tasks chan probeTask
	done  chan probeDone

	// Statistics
	submitted int64
	completed int64

	// Address reservation is owned by the scheduling goroutine, before dispatch.
	seenIPs        map[netip.Addr]struct{}
	nextIPs        map[netip.Prefix]netip.Addr
	exhausted      map[netip.Prefix]bool
	successful     int64
	failed         int64
	requests       int64
	spaceExhausted bool
	seed           int64
}

var errPrefixExhausted = errors.New("prefix exhausted")
var errSearchSpaceExhausted = errors.New("search space exhausted")

type probeTask struct {
	headID int
	prefix netip.Prefix
	ip     netip.Addr
}

type probeDone struct {
	task   probeTask
	result probe.Result
}

// New preserves the supplied configuration. Start with DefaultConfig and apply
// explicit overrides; Run validates the resulting configuration before work.
func New(cfg Config, probeCfg probe.Config) *Engine {
	return &Engine{
		cfg:      cfg,
		probeCfg: probeCfg,
	}
}

// Run executes the search with the given CIDRs.
func (e *Engine) Run(ctx context.Context, req Request) (Response, error) {
	if err := e.cfg.Validate(); err != nil {
		return Response{}, err
	}

	// Load prefixes
	prefixes, err := loadPrefixes(req)
	if err != nil {
		return Response{}, err
	}
	if len(prefixes) == 0 {
		return Response{}, errors.New("no CIDR provided (use --cidr or --cidr-file)")
	}
	e.submitted, e.completed, e.successful, e.failed, e.requests = 0, 0, 0, 0, 0
	e.spaceExhausted = false
	e.seenIPs = make(map[netip.Addr]struct{})
	e.nextIPs = make(map[netip.Prefix]netip.Addr)
	e.exhausted = make(map[netip.Prefix]bool)
	if req.Probe.Timeout <= 0 {
		req.Probe.Timeout = 3 * time.Second
	}

	// Initialize seed
	seed := e.cfg.Seed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	e.seed = seed

	// Initialize components
	timeoutMS := req.TimeoutMS()
	e.tree = bandit.NewArmTree(prefixes, e.cfg.ToTreeConfig())
	headCfg := e.cfg.ToHeadManagerConfig(timeoutMS)
	headCfg.BaseSeed = seed
	e.headManager = bandit.NewHeadManager(headCfg)
	e.topN = NewTopNCollector(min(e.cfg.TopN, e.cfg.Budget))

	// Initialize channels
	e.tasks = make(chan probeTask, e.cfg.Concurrency*2)
	e.done = make(chan probeDone, e.cfg.Concurrency*2)

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < e.cfg.Concurrency; i++ {
		wg.Add(1)
		go e.worker(ctx, &wg, req.Probe)
	}

	// Run main event-driven scheduling loop
	err = e.schedule(ctx, timeoutMS)

	// Cleanup
	close(e.tasks)
	wg.Wait()
	close(e.done)

	// Drain any remaining results
	for d := range e.done {
		e.processOneResult(d, timeoutMS)
		atomic.AddInt64(&e.completed, 1)
	}

	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return Response{}, err
	}

	stats := RunStats{
		Budget: e.cfg.Budget, UniqueIPs: e.submitted, Completed: e.completed,
		Successful: e.successful, Failed: e.failed, Exhausted: e.spaceExhausted,
		RequestAttempts: e.requests,
		Seed:            e.seed,
	}
	if e.cfg.Verbose {
		fmt.Fprintf(os.Stderr, "summary: unique_ips=%d request_attempts=%d completed=%d successful=%d failed=%d exhausted=%v seed=%d\n",
			stats.UniqueIPs, stats.RequestAttempts, stats.Completed, stats.Successful, stats.Failed, stats.Exhausted, stats.Seed)
	}
	return Response{Top: e.topN.Snapshot(), Stats: stats}, nil
}

// schedule is the main event-driven scheduling loop.
func (e *Engine) schedule(ctx context.Context, timeoutMS float64) error {
	start := time.Now()
	lastLog := time.Now()
	lastSplit := int64(0)

	// Initial fill - submit initial batch of tasks
	initialBatch := e.cfg.Concurrency * 2
	if initialBatch > e.cfg.Budget {
		initialBatch = e.cfg.Budget
	}

	for i := 0; i < initialBatch; i++ {
		headID := i % e.cfg.Heads
		if err := e.submitOneTask(ctx, headID); err != nil {
			if errors.Is(err, errSearchSpaceExhausted) {
				e.spaceExhausted = true
				break
			}
			return err
		}
	}

	// Main event loop - process results and submit new tasks
	// A finite input may contain fewer unique addresses than the budget. Drain
	// only submitted work; never wait for results from tasks that do not exist.
	for atomic.LoadInt64(&e.completed) < atomic.LoadInt64(&e.submitted) {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case d := <-e.done:
			// Process the completed probe
			e.processOneResult(d, timeoutMS)
			completed := atomic.AddInt64(&e.completed, 1)

			// Check if we need to split - more aggressive splitting
			if completed-lastSplit >= int64(e.cfg.SplitInterval) {
				e.trySplit()
				lastSplit = completed
			}

			// Submit replacement task if we haven't reached budget
			submitted := atomic.LoadInt64(&e.submitted)
			if !e.spaceExhausted && submitted < int64(e.cfg.Budget) {
				headID := int(submitted) % e.cfg.Heads
				if err := e.submitOneTask(ctx, headID); err != nil {
					if errors.Is(err, errSearchSpaceExhausted) {
						e.spaceExhausted = true
					} else {
						return err
					}
				}
			}

			// Verbose logging
			if e.cfg.Verbose && time.Since(lastLog) > time.Second {
				best := e.topN.Best()
				elapsed := time.Since(start).Truncate(100 * time.Millisecond)
				fmt.Fprintf(os.Stderr, "progress: %d/%d done, best=%.1fms ip=%s prefix=%s elapsed=%s nodes=%d\n",
					completed, e.cfg.Budget, best.ScoreMS, best.IP.String(), best.Prefix.String(), elapsed, e.tree.Size())
				lastLog = time.Now()
			}
		}
	}

	return nil
}

// submitOneTask submits a single probe task for a head.
func (e *Engine) submitOneTask(ctx context.Context, headID int) error {
	head := e.headManager.GetHead(headID % e.cfg.Heads)
	if head == nil {
		return errors.New("search head unavailable")
	}

	var prefix netip.Prefix

	// Exploitation mode: directly sample from known-good prefixes
	// This ensures we find multiple IPs from the best regions
	completed := atomic.LoadInt64(&e.completed)
	budget := int64(e.cfg.Budget)

	// Gradually increase exploitation rate as we progress
	// Early: 20% exploit, Late: 50% exploit
	exploitRate := 0.2 + 0.3*float64(completed)/float64(budget)
	if exploitRate > 0.5 {
		exploitRate = 0.5
	}

	if completed > 30 { // Only after initial exploration
		exploitPrefixes := e.getExploitationPrefixes()
		if len(exploitPrefixes) > 0 && head.Sampler != nil {
			if r := head.Sampler.SampleUniform(); r < exploitRate {
				// Pick a random prefix from exploit list, weighted toward better ones
				idx := int(r / exploitRate * float64(len(exploitPrefixes)))
				if idx >= len(exploitPrefixes) {
					idx = len(exploitPrefixes) - 1
				}
				prefix = exploitPrefixes[idx]
			}
		}
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !prefix.IsValid() || e.exhausted[prefix] {
			prefix = e.headManager.SelectNextAvailablePrefix(head, e.tree, e.cfg.Beam,
				func(p netip.Prefix) bool { return !e.exhausted[p] })
		}
		if !prefix.IsValid() {
			return errSearchSpaceExhausted
		}
		ip, err := e.sampleIPWithDedup(ctx, prefix, head)
		if errors.Is(err, errPrefixExhausted) {
			e.tree.RetirePrefix(prefix)
			prefix = netip.Prefix{}
			continue
		}
		if err != nil {
			return err
		}
		select {
		case e.tasks <- probeTask{headID: headID, prefix: prefix, ip: ip}:
			atomic.AddInt64(&e.submitted, 1)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// passColoFilter returns true if the result with the given colo should enter TopN.
// Empty colo is treated as "no colo". When both ColoAllow and ColoBlock are empty, all pass.
func (e *Engine) passColoFilter(colo string) bool {
	if len(e.cfg.ColoAllow) > 0 {
		for _, c := range e.cfg.ColoAllow {
			if c == colo {
				return true
			}
		}
		return false
	}
	if len(e.cfg.ColoBlock) > 0 {
		for _, c := range e.cfg.ColoBlock {
			if c == colo {
				return false
			}
		}
		return true
	}
	return true
}

// processOneResult processes a single probe result.
func (e *Engine) processOneResult(d probeDone, timeoutMS float64) {
	e.requests += int64(d.result.Requests)
	// Update arm tree with result
	e.tree.Update(d.task.prefix, d.result.OK, float64(d.result.TotalMS), timeoutMS)
	if !d.result.OK {
		e.failed++
		return
	}
	e.successful++

	// Get arm stats
	node := e.tree.GetNode(d.task.prefix)
	var stats bandit.ArmStats
	if node != nil {
		stats = node.Stats()
	}

	// Colo filter: only consider for TopN if colo passes
	colo := ""
	if d.result.Trace != nil {
		colo = d.result.Trace["colo"]
	}
	if !e.passColoFilter(colo) {
		return
	}

	// Failures remain in arm statistics, not in the usable-IP ranking.
	score := float64(d.result.TotalMS)

	// Add to top N
	e.topN.Consider(TopResult{
		IP:            d.task.ip,
		Prefix:        d.task.prefix,
		OK:            d.result.OK,
		Status:        d.result.Status,
		Error:         d.result.Error,
		ConnectMS:     d.result.ConnectMS,
		TLSMS:         d.result.TLSMS,
		TTFBMS:        d.result.TTFBMS,
		TotalMS:       d.result.TotalMS,
		ScoreMS:       score,
		Trace:         d.result.Trace,
		PrefixSamples: stats.Samples,
		PrefixOK:      stats.Successes,
		PrefixFail:    stats.Failures,
	})
}

// worker runs probe tasks.
func (e *Engine) worker(ctx context.Context, wg *sync.WaitGroup, probeCfg probe.Config) {
	defer wg.Done()

	prober := probe.NewProber(probeCfg)
	defer prober.Close()

	// Calculate timeout for multiple rounds
	rounds := probeCfg.Rounds
	if rounds <= 0 {
		rounds = 6
	}
	multiTimeout := probeCfg.Timeout * time.Duration(rounds)

	for task := range e.tasks {
		pctx, cancel := context.WithTimeout(ctx, multiTimeout)
		result := prober.ProbeHTTPTraceMulti(pctx, task.ip)
		cancel()

		select {
		case e.done <- probeDone{task: task, result: result}:
		case <-ctx.Done():
			return
		}
	}
}

// trySplit attempts to split promising prefixes.
// It prioritizes nodes with good performance (low latency, high success rate).
func (e *Engine) trySplit() {
	if !e.tree.CanExpand() {
		return
	}
	// Get more candidates - be more aggressive about splitting
	candidates := e.tree.GetSplitCandidates(e.cfg.Heads * 4)

	splitCount := 0
	maxSplits := e.cfg.Heads * 2

	for _, node := range candidates {
		if splitCount >= maxSplits {
			break
		}
		if e.tree.SplitNode(node) != nil {
			splitCount++
		}
	}

	// Periodically rebalance heads to explore new areas
	e.headManager.RebalanceHeads(e.tree)
}

// getExploitationPrefixes returns prefixes that deserve intensive exploitation.
// These are prefixes containing top-performing IPs that we should sample more from.
// Returns prefixes sorted by best score (best first), with repeats for weighting.
func (e *Engine) getExploitationPrefixes() []netip.Prefix {
	topResults := e.topN.Snapshot()
	if len(topResults) == 0 {
		return nil
	}

	// Calculate thresholds
	bestScore := topResults[0].ScoreMS
	tier1Threshold := bestScore * 1.2 // Within 20% of best
	tier2Threshold := bestScore * 1.5 // Within 50% of best

	// Snapshot order is stable for a fixed result stream. Do not assign random
	// draws to map iteration order when choosing exploitation prefixes.
	seen := make(map[netip.Prefix]bool)
	var exploitPrefixes []netip.Prefix
	for _, r := range topResults {
		if r.ScoreMS > tier2Threshold {
			break
		}
		if seen[r.Prefix] {
			continue
		}
		seen[r.Prefix] = true
		if r.ScoreMS <= tier1Threshold {
			// Best prefixes get 3x weight
			exploitPrefixes = append(exploitPrefixes, r.Prefix, r.Prefix, r.Prefix)
		} else {
			// Good prefixes get 1x weight
			exploitPrefixes = append(exploitPrefixes, r.Prefix)
		}
	}

	return exploitPrefixes
}

// sampleIPWithDedup samples an IP with deduplication.
func (e *Engine) sampleIPWithDedup(ctx context.Context, prefix netip.Prefix, head *bandit.SearchHead) (netip.Addr, error) {
	prefix = prefix.Masked()
	if err := ctx.Err(); err != nil {
		return netip.Addr{}, err
	}
	if e.exhausted[prefix] {
		return netip.Addr{}, errPrefixExhausted
	}
	// Fast random path. Reservation also covers /32 and /128 singletons.
	const maxTries = 32
	for i := 0; i < maxTries; i++ {
		ip := head.Sampler.SampleIP(prefix)
		if _, seen := e.seenIPs[ip]; !seen {
			e.seenIPs[ip] = struct{}{}
			return ip, nil
		}
	}
	// Collisions are not evidence of exhaustion. A monotonic fallback cursor
	// finds the next unused address, or proves that this prefix is exhausted.
	// This needs no address-space-sized bitmap (including for IPv6).
	ip, exists := e.nextIPs[prefix]
	if !exists {
		ip = prefix.Addr()
	}
	for ip.IsValid() && prefix.Contains(ip) {
		if err := ctx.Err(); err != nil {
			return netip.Addr{}, err
		}
		if _, seen := e.seenIPs[ip]; !seen {
			e.seenIPs[ip] = struct{}{}
			e.nextIPs[prefix] = ip.Next()
			return ip, nil
		}
		ip = ip.Next()
	}
	e.exhausted[prefix] = true
	return netip.Addr{}, errPrefixExhausted
}

// loadPrefixes loads and deduplicates CIDR prefixes from the request.
func loadPrefixes(req Request) ([]netip.Prefix, error) {
	var pfxs []netip.Prefix

	if len(req.CIDRs) > 0 {
		ps, err := cidr.ParseCIDRs(req.CIDRs)
		if err != nil {
			return nil, err
		}
		pfxs = append(pfxs, ps...)
	}

	if req.CIDRFile != "" {
		ps, err := cidr.ReadCIDRsFromFile(req.CIDRFile)
		if err != nil {
			return nil, err
		}
		pfxs = append(pfxs, ps...)
	}

	return cidr.RemoveContained(pfxs), nil
}
