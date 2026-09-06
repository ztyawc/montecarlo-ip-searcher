package bandit

import (
	"net/netip"
	"sort"
	"sync"

	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/cidr"
)

const (
	maxSplitStep = 8
	// Limit a single expansion to a small, bounded frontier. Larger configured
	// steps are applied across successive splits instead of allocating 2^16 arms.
	MaxChildrenPerSplit = 1 << maxSplitStep
	// At roughly a few hundred bytes per arm plus indexes, this bounds dynamic
	// expansion to tens of MiB. Input roots are always retained, even above it.
	DefaultMaxTreeNodes = 65536
)

// ArmTree manages a hierarchical tree of arm nodes organized by CIDR prefixes.
// It supports efficient lookup, traversal, and dynamic splitting.
type ArmTree struct {
	roots     []*ArmNode
	nodeMap   map[netip.Prefix]*ArmNode
	nodes     []*ArmNode
	leaves    []*ArmNode
	leafIndex map[netip.Prefix]int
	mu        sync.RWMutex

	// Configuration
	splitStepV4 int
	splitStepV6 int
	maxBitsV4   int
	maxBitsV6   int
	minSamples  int
	maxNodes    int
}

// TreeConfig holds configuration for the arm tree.
type TreeConfig struct {
	SplitStepV4 int // Prefix bits to add when splitting IPv4
	SplitStepV6 int // Prefix bits to add when splitting IPv6
	MaxBitsV4   int // Maximum prefix length for IPv4
	MaxBitsV6   int // Maximum prefix length for IPv6
	MinSamples  int // Minimum samples before splitting
	MaxNodes    int // Total nodes including parents; 0 uses DefaultMaxTreeNodes
}

// DefaultTreeConfig returns sensible defaults.
func DefaultTreeConfig() TreeConfig {
	return TreeConfig{
		SplitStepV4: 2,
		SplitStepV6: 4,
		MaxBitsV4:   24,
		MaxBitsV6:   56,
		MinSamples:  5, // Lower for faster drill-down
		MaxNodes:    DefaultMaxTreeNodes,
	}
}

// NewArmTree creates a new arm tree with the given root prefixes.
func NewArmTree(prefixes []netip.Prefix, cfg TreeConfig) *ArmTree {
	prefixes = cidr.RemoveContained(prefixes)
	maxNodes := cfg.MaxNodes
	if maxNodes == 0 {
		maxNodes = DefaultMaxTreeNodes
	}
	maxNodes = max(maxNodes, len(prefixes))
	t := &ArmTree{
		roots:       make([]*ArmNode, 0, len(prefixes)),
		nodeMap:     make(map[netip.Prefix]*ArmNode, len(prefixes)),
		nodes:       make([]*ArmNode, 0, len(prefixes)),
		leaves:      make([]*ArmNode, 0, len(prefixes)),
		leafIndex:   make(map[netip.Prefix]int, len(prefixes)),
		splitStepV4: cfg.SplitStepV4,
		splitStepV6: cfg.SplitStepV6,
		maxBitsV4:   cfg.MaxBitsV4,
		maxBitsV6:   cfg.MaxBitsV6,
		minSamples:  cfg.MinSamples,
		maxNodes:    maxNodes,
	}

	for _, p := range prefixes {
		p = p.Masked()
		if _, exists := t.nodeMap[p]; exists {
			continue
		}
		node := NewArmNode(p, nil)
		t.roots = append(t.roots, node)
		t.addNodeLocked(node)
	}

	return t
}

// GetNode returns the arm node for the given prefix, or nil if not found.
func (t *ArmTree) GetNode(prefix netip.Prefix) *ArmNode {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.nodeMap[prefix.Masked()]
}

// GetOrCreateNode creates a missing node when capacity permits, or returns nil.
func (t *ArmTree) GetOrCreateNode(prefix netip.Prefix) *ArmNode {
	prefix = prefix.Masked()
	if !prefix.IsValid() {
		return nil
	}

	t.mu.RLock()
	if node, exists := t.nodeMap[prefix]; exists {
		t.mu.RUnlock()
		return node
	}
	t.mu.RUnlock()

	t.mu.Lock()
	defer t.mu.Unlock()

	// Double-check after acquiring write lock
	if node, exists := t.nodeMap[prefix]; exists {
		return node
	}
	if len(t.nodes) >= t.maxNodes {
		return nil
	}

	// Find parent
	var parent *ArmNode
	for _, root := range t.roots {
		if root.Prefix.Contains(prefix.Addr()) && root.Prefix.Bits() < prefix.Bits() {
			parent = t.findParentLocked(root, prefix)
			break
		}
	}

	node := NewArmNode(prefix, parent)
	t.addNodeLocked(node)

	if parent != nil {
		parent.AddChild(node)
	} else {
		t.roots = append(t.roots, node)
	}

	return node
}

// findParentLocked finds the immediate parent of a prefix within a subtree.
// Must be called with write lock held.
func (t *ArmTree) findParentLocked(node *ArmNode, target netip.Prefix) *ArmNode {
	if !node.Prefix.Contains(target.Addr()) {
		return nil
	}

	// Check children for a closer parent
	node.mu.RLock()
	children := node.Children
	node.mu.RUnlock()

	for _, child := range children {
		if child.Prefix.Contains(target.Addr()) && child.Prefix.Bits() < target.Bits() {
			return t.findParentLocked(child, target)
		}
	}

	return node
}

// AllNodes returns all nodes in the tree.
func (t *ArmTree) AllNodes() []*ArmNode {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return append([]*ArmNode(nil), t.nodes...)
}

// LeafNodes returns a deterministic snapshot of selectable leaves.
func (t *ArmTree) LeafNodes() []*ArmNode {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return append([]*ArmNode(nil), t.leaves...)
}

func (t *ArmTree) LeafCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.leaves)
}

// CanExpand reports whether at least a binary split fits in the node budget.
func (t *ArmTree) CanExpand() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.maxNodes-len(t.nodes) >= 2
}

func (t *ArmTree) addNodeLocked(node *ArmNode) {
	t.nodeMap[node.Prefix] = node
	t.nodes = append(t.nodes, node)
	t.leafIndex[node.Prefix] = len(t.leaves)
	t.leaves = append(t.leaves, node)
}

func (t *ArmTree) removeLeafLocked(prefix netip.Prefix) {
	idx, ok := t.leafIndex[prefix]
	if !ok {
		return
	}
	last := len(t.leaves) - 1
	t.leaves[idx] = t.leaves[last]
	t.leafIndex[t.leaves[idx].Prefix] = idx
	t.leaves[last] = nil
	t.leaves = t.leaves[:last]
	delete(t.leafIndex, prefix)
}

// RetirePrefix excludes an exhausted leaf without discarding its statistics.
func (t *ArmTree) RetirePrefix(prefix netip.Prefix) {
	t.mu.Lock()
	defer t.mu.Unlock()
	prefix = prefix.Masked()
	if node := t.nodeMap[prefix]; node != nil {
		node.mu.Lock()
		node.retired = true
		node.mu.Unlock()
		t.removeLeafLocked(prefix)
	}
}

// leafWalk is an implicit permutation, requiring constant memory per head.
// A coprime stride visits every leaf once while the frontier size is unchanged.
type leafWalk struct {
	count, index, stride int
}

func (t *ArmTree) nextLeaf(walk *leafWalk, sampler *ThompsonSampler, accept func(*ArmNode) bool) *ArmNode {
	t.mu.RLock()
	defer t.mu.RUnlock()
	n := len(t.leaves)
	if n == 0 {
		return nil
	}
	if walk.count != n {
		walk.count, walk.index = n, sampler.SampleIndex(n)
		walk.stride = 1
		if n > 1 {
			walk.stride = 1 + sampler.SampleIndex(n-1)
			for gcd(walk.stride, n) != 1 {
				walk.stride++
			}
		}
	}
	for visited := 0; visited < n; visited++ {
		node := t.leaves[walk.index]
		walk.index = (walk.index + walk.stride) % n
		if accept(node) {
			return node
		}
	}
	return nil
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// SplitNode splits a node into child prefixes.
// Returns the created children, or nil if split is not possible.
func (t *ArmTree) SplitNode(node *ArmNode) []*ArmNode {
	if node == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.nodeMap[node.Prefix] != node || !node.CanSplit(t.minSamples, t.maxBitsV4, t.maxBitsV6) {
		return nil
	}

	prefix := node.Prefix
	step := t.splitStepV6
	maxBits := min(t.maxBitsV6, 128)
	if prefix.Addr().Is4() {
		step = t.splitStepV4
		maxBits = min(t.maxBitsV4, 32)
	}
	step = min(step, maxBits-prefix.Bits(), maxSplitStep)
	remaining := t.maxNodes - len(t.nodes)
	for step > 0 && 1<<step > remaining {
		step--
	}
	if step <= 0 {
		return nil
	}

	children, err := cidr.SplitPrefix(prefix, step)
	if err != nil || len(children) == 0 {
		return nil
	}

	createdChildren := make([]*ArmNode, 0, len(children))
	for _, childPrefix := range children {
		childPrefix = childPrefix.Masked()
		if _, exists := t.nodeMap[childPrefix]; exists {
			continue
		}

		childNode := NewArmNode(childPrefix, node)
		t.addNodeLocked(childNode)
		node.AddChild(childNode)
		createdChildren = append(createdChildren, childNode)
	}

	node.MarkSplit()
	t.removeLeafLocked(prefix)
	return createdChildren
}

// GetSplitCandidates returns nodes that are candidates for splitting,
// sorted by a combination of performance (good nodes first) and uncertainty.
// This ensures we drill down into promising regions while also exploring uncertain ones.
func (t *ArmTree) GetSplitCandidates(limit int) []*ArmNode {
	if limit <= 0 || !t.CanExpand() {
		return nil
	}
	leaves := t.LeafNodes()

	type candidate struct {
		node     *ArmNode
		priority float64 // Lower is better (higher priority for splitting)
	}

	candidates := make([]candidate, 0, len(leaves))
	for _, node := range leaves {
		if node.CanSplit(t.minSamples, t.maxBitsV4, t.maxBitsV6) {
			stats := node.Stats()

			// Priority formula:
			// - Low latency = high priority (we want to drill into fast regions)
			// - High success rate = high priority
			// - High uncertainty = moderate boost (explore unknowns)

			// Base priority is mean latency (lower = better)
			latencyScore := stats.MeanLatency
			if stats.Successes == 0 {
				latencyScore = 10000 // Penalty for no successes
			}

			// Bonus for high success rate (up to 500ms reduction)
			successBonus := stats.SuccessRate * 500

			// Bonus for uncertainty (encourage exploring uncertain nodes)
			uncertaintyBonus := node.InformationGain() * 50

			priority := latencyScore - successBonus - uncertaintyBonus

			candidates = append(candidates, candidate{
				node:     node,
				priority: priority,
			})
		}
	}

	// Sort by priority (lowest first = best candidates)
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].priority == candidates[j].priority {
			return prefixLess(candidates[i].node.Prefix, candidates[j].node.Prefix)
		}
		return candidates[i].priority < candidates[j].priority
	})

	if limit > len(candidates) {
		limit = len(candidates)
	}

	result := make([]*ArmNode, limit)
	for i := 0; i < limit; i++ {
		result[i] = candidates[i].node
	}
	return result
}

// Update updates the statistics for a prefix.
func (t *ArmTree) Update(prefix netip.Prefix, success bool, latencyMS, timeoutMS float64) {
	node := t.GetOrCreateNode(prefix)
	if node != nil {
		node.Update(success, latencyMS, timeoutMS)
	}
}

// Roots returns the root nodes.
func (t *ArmTree) Roots() []*ArmNode {
	t.mu.RLock()
	defer t.mu.RUnlock()
	roots := make([]*ArmNode, len(t.roots))
	copy(roots, t.roots)
	return roots
}

// Size returns the total number of nodes in the tree.
func (t *ArmTree) Size() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.nodeMap)
}

// TotalSamples returns the total number of samples across all nodes.
func (t *ArmTree) TotalSamples() int {
	t.mu.RLock()
	defer t.mu.RUnlock()

	total := 0
	for _, node := range t.nodes {
		stats := node.Stats()
		total += stats.Samples
	}
	return total
}

func prefixLess(a, b netip.Prefix) bool {
	if cmp := a.Addr().Compare(b.Addr()); cmp != 0 {
		return cmp < 0
	}
	return a.Bits() < b.Bits()
}
