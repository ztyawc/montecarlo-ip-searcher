package bandit

import (
	"math"
	"net/netip"
	"sort"
	"sync"
)

// SearchHead represents a single search head in multi-head search.
// Each head maintains its own sampler and focus area for diversity.
type SearchHead struct {
	ID      int
	Sampler *ThompsonSampler

	// Current focus area (the prefix this head is exploring)
	CurrentFocus netip.Prefix

	// History of explored prefixes (for diversity computation)
	History     []netip.Prefix
	historySize int
	historyNext int

	// Selection state is separate from the focus lock, so heads may inspect each
	// other's focus without acquiring each other's candidate locks.
	selectionMu  sync.Mutex
	beamTree     *ArmTree
	beamWidth    int
	candidates   []scoredCandidate
	candidateSet map[*ArmNode]struct{}
	walk         leafWalk

	mu sync.RWMutex
}

// NewSearchHead creates a new search head.
func NewSearchHead(id int, seed int64, timeoutMS float64, historySize int) *SearchHead {
	historySize = max(historySize, 0)
	return &SearchHead{
		ID:          id,
		Sampler:     NewThompsonSampler(seed, timeoutMS),
		History:     make([]netip.Prefix, 0, historySize),
		historySize: historySize,
	}
}

// SetFocus updates the current focus prefix.
func (h *SearchHead) SetFocus(prefix netip.Prefix) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.CurrentFocus = prefix
	if h.historySize == 0 {
		return
	}
	if len(h.History) < h.historySize {
		h.History = append(h.History, prefix)
	} else {
		h.History[h.historyNext] = prefix
		h.historyNext = (h.historyNext + 1) % h.historySize
	}
}

// GetFocus returns the current focus prefix.
func (h *SearchHead) GetFocus() netip.Prefix {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.CurrentFocus
}

// GetHistory returns a copy of the exploration history.
func (h *SearchHead) GetHistory() []netip.Prefix {
	h.mu.RLock()
	defer h.mu.RUnlock()
	result := make([]netip.Prefix, len(h.History))
	if len(h.History) < h.historySize || h.historyNext == 0 {
		copy(result, h.History)
	} else {
		n := copy(result, h.History[h.historyNext:])
		copy(result[n:], h.History[:h.historyNext])
	}
	return result
}

// HeadManager manages multiple search heads with diversity preservation.
type HeadManager struct {
	heads []*SearchHead
	mu    sync.RWMutex

	// Diversity parameters
	diversityWeight float64 // Weight for diversity penalty
	repulsionDecay  float64 // Decay factor for distance-based repulsion
}

// HeadManagerConfig holds configuration for the head manager.
type HeadManagerConfig struct {
	NumHeads        int
	TimeoutMS       float64
	BaseSeed        int64
	HistorySize     int
	DiversityWeight float64
	RepulsionDecay  float64
}

// DefaultHeadManagerConfig returns sensible defaults.
func DefaultHeadManagerConfig() HeadManagerConfig {
	return HeadManagerConfig{
		NumHeads:        4,
		TimeoutMS:       3000,
		BaseSeed:        0,
		HistorySize:     32,
		DiversityWeight: 0.3,
		RepulsionDecay:  0.5,
	}
}

// NewHeadManager creates a new head manager with the specified number of heads.
func NewHeadManager(cfg HeadManagerConfig) *HeadManager {
	heads := make([]*SearchHead, cfg.NumHeads)
	for i := 0; i < cfg.NumHeads; i++ {
		// Each head gets a different seed for independent sampling
		seed := cfg.BaseSeed + int64(i*9973)
		heads[i] = NewSearchHead(i, seed, cfg.TimeoutMS, cfg.HistorySize)
	}

	return &HeadManager{
		heads:           heads,
		diversityWeight: cfg.DiversityWeight,
		repulsionDecay:  cfg.RepulsionDecay,
	}
}

// NumHeads returns the number of search heads.
func (m *HeadManager) NumHeads() int {
	return len(m.heads)
}

// GetHead returns the head at the given index.
func (m *HeadManager) GetHead(idx int) *SearchHead {
	if idx < 0 || idx >= len(m.heads) {
		return nil
	}
	return m.heads[idx]
}

type scoredCandidate struct {
	node  *ArmNode
	score float64
}

// SelectNextPrefix scores at most beamWidth retained candidates. One slot is
// refreshed on each subsequent selection, keeping global exploration alive.
func (m *HeadManager) SelectNextPrefix(head *SearchHead, tree *ArmTree, beamWidth int) netip.Prefix {
	return m.SelectNextAvailablePrefix(head, tree, beamWidth, nil)
}

// SelectNextAvailablePrefix also removes exhausted or split cached candidates.
func (m *HeadManager) SelectNextAvailablePrefix(head *SearchHead, tree *ArmTree, beamWidth int, available func(netip.Prefix) bool) netip.Prefix {
	if head == nil || tree == nil || beamWidth <= 0 {
		return netip.Prefix{}
	}
	head.selectionMu.Lock()
	defer head.selectionMu.Unlock()
	m.prepareCandidates(head, tree, beamWidth, available)
	return m.scoreCandidates(head)
}

// prepareCandidates keeps good candidates while rotating in one fresh leaf.
// Its steady-state work is bounded by the beam, not the total tree size.
// The caller owns head.selectionMu.
func (m *HeadManager) prepareCandidates(head *SearchHead, tree *ArmTree, beamWidth int, available func(netip.Prefix) bool) {
	target := min(beamWidth, tree.LeafCount())
	if head.beamTree != tree || head.beamWidth != beamWidth {
		head.beamTree, head.beamWidth = tree, beamWidth
		head.candidates = make([]scoredCandidate, 0, target)
		head.candidateSet = make(map[*ArmNode]struct{}, target)
		head.walk = leafWalk{}
	}
	kept := head.candidates[:0]
	for _, c := range head.candidates {
		if c.node.selectable() && (available == nil || available(c.node.Prefix)) {
			kept = append(kept, c)
		} else {
			delete(head.candidateSet, c.node)
		}
	}
	head.candidates = kept
	accept := func(node *ArmNode) bool {
		if _, retained := head.candidateSet[node]; retained {
			return false
		}
		return node.selectable() && (available == nil || available(node.Prefix))
	}
	refill := len(head.candidates) < target
	for len(head.candidates) < target {
		node := tree.nextLeaf(&head.walk, head.Sampler, accept)
		if node == nil {
			break
		}
		head.candidates = append(head.candidates, scoredCandidate{node: node})
		head.candidateSet[node] = struct{}{}
	}
	if refill || len(head.candidates) == 0 || tree.LeafCount() <= len(head.candidates) {
		return
	}
	if node := tree.nextLeaf(&head.walk, head.Sampler, accept); node != nil {
		worst := 0
		for i := 1; i < len(head.candidates); i++ {
			if candidateLess(head.candidates[worst], head.candidates[i]) {
				worst = i
			}
		}
		delete(head.candidateSet, head.candidates[worst].node)
		head.candidates[worst] = scoredCandidate{node: node}
		head.candidateSet[node] = struct{}{}
	}
}

func candidateLess(a, b scoredCandidate) bool {
	if a.score == b.score {
		return prefixLess(a.node.Prefix, b.node.Prefix)
	}
	return a.score < b.score
}

func (m *HeadManager) scoreCandidates(head *SearchHead) netip.Prefix {
	if len(head.candidates) == 0 {
		return netip.Prefix{}
	}
	otherFocuses := m.getOtherHeadFocuses(head.ID)
	best := 0
	for i := range head.candidates {
		c := &head.candidates[i]
		tsScore := head.Sampler.SampleScore(c.node)
		penalty := m.computeDiversityPenalty(c.node.Prefix, otherFocuses)
		bits := c.node.Prefix.Bits()
		depthBonus := float64(bits-32) / 24.0 * 0.2
		if c.node.Prefix.Addr().Is4() {
			depthBonus = float64(bits-16) / 8.0 * 0.2
		}
		c.score = tsScore * (1 + m.diversityWeight*penalty) * (1 - max(depthBonus, 0))
		if candidateLess(*c, head.candidates[best]) {
			best = i
		}
	}
	prefix := head.candidates[best].node.Prefix
	head.SetFocus(prefix)
	return prefix
}

// SelectBeam returns the current bounded candidates in sampled-score order.
func (m *HeadManager) SelectBeam(head *SearchHead, tree *ArmTree, beamWidth int) []netip.Prefix {
	if head == nil || tree == nil || beamWidth <= 0 {
		return nil
	}
	head.selectionMu.Lock()
	defer head.selectionMu.Unlock()
	m.prepareCandidates(head, tree, beamWidth, nil)
	m.scoreCandidates(head)
	sort.Slice(head.candidates, func(i, j int) bool { return candidateLess(head.candidates[i], head.candidates[j]) })
	result := make([]netip.Prefix, len(head.candidates))
	for i, c := range head.candidates {
		result[i] = c.node.Prefix
	}
	return result
}

// getOtherHeadFocuses returns the current focus of all other heads.
func (m *HeadManager) getOtherHeadFocuses(excludeID int) []netip.Prefix {
	m.mu.RLock()
	defer m.mu.RUnlock()

	focuses := make([]netip.Prefix, 0, len(m.heads)-1)
	for _, head := range m.heads {
		if head.ID != excludeID {
			focus := head.GetFocus()
			if focus.IsValid() {
				focuses = append(focuses, focus)
			}
		}
	}
	return focuses
}

// computeDiversityPenalty computes a penalty based on proximity to other heads.
// Higher penalty = closer to other heads = should be avoided.
func (m *HeadManager) computeDiversityPenalty(prefix netip.Prefix, otherFocuses []netip.Prefix) float64 {
	if len(otherFocuses) == 0 {
		return 0
	}

	var totalPenalty float64
	for _, other := range otherFocuses {
		distance := prefixDistance(prefix, other)
		if distance == 0 {
			// Same prefix: maximum penalty
			totalPenalty += 1.0
		} else {
			// Inverse distance with decay
			totalPenalty += math.Pow(m.repulsionDecay, float64(distance))
		}
	}

	return totalPenalty / float64(len(otherFocuses))
}

// prefixDistance computes a distance metric between two prefixes.
// 0 = identical, larger = more different.
func prefixDistance(a, b netip.Prefix) int {
	// Different address families: maximum distance
	if a.Addr().Is4() != b.Addr().Is4() {
		return 128
	}

	// Find the common prefix length
	aBits := a.Bits()
	bBits := b.Bits()
	minBits := aBits
	if bBits < minBits {
		minBits = bBits
	}

	// Compare the network portions
	if a.Addr().Is4() {
		aBytes := a.Addr().As4()
		bBytes := b.Addr().As4()
		return compareBytes(aBytes[:], bBytes[:], minBits)
	}

	aBytes := a.Addr().As16()
	bBytes := b.Addr().As16()
	return compareBytes(aBytes[:], bBytes[:], minBits)
}

// compareBytes returns the number of matching prefix bits.
func compareBytes(a, b []byte, maxBits int) int {
	matching := 0
	for i := 0; i < len(a) && matching < maxBits; i++ {
		xor := a[i] ^ b[i]
		if xor == 0 {
			matching += 8
			if matching > maxBits {
				matching = maxBits
			}
		} else {
			// Count leading zeros in XOR
			for bit := 7; bit >= 0 && matching < maxBits; bit-- {
				if (xor>>uint(bit))&1 == 0 {
					matching++
				} else {
					break
				}
			}
			break
		}
	}

	// Distance = maxBits - matching
	return maxBits - matching
}

// RebalanceHeads reassigns heads to different areas if they've converged.
func (m *HeadManager) RebalanceHeads(tree *ArmTree) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if heads have converged (all exploring similar areas)
	focuses := make([]netip.Prefix, 0, len(m.heads))
	for _, head := range m.heads {
		focus := head.GetFocus()
		if focus.IsValid() {
			focuses = append(focuses, focus)
		}
	}

	if len(focuses) < 2 {
		return
	}

	// Compute pairwise distances
	var totalDistance int
	pairs := 0
	for i := 0; i < len(focuses); i++ {
		for j := i + 1; j < len(focuses); j++ {
			totalDistance += prefixDistance(focuses[i], focuses[j])
			pairs++
		}
	}

	if pairs == 0 {
		return
	}

	avgDistance := float64(totalDistance) / float64(pairs)

	// If average distance is too low, force rebalancing
	// Threshold: less than 4 bits of difference on average
	if avgDistance < 4 {
		leaves := tree.LeafNodes()
		if len(leaves) < len(m.heads) {
			return
		}

		// Assign each head to a different part of the search space
		for i, head := range m.heads {
			idx := (i * len(leaves)) / len(m.heads)
			head.SetFocus(leaves[idx].Prefix)
		}
	}
}
