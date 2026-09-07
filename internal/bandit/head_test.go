package bandit

import (
	"net/netip"
	"reflect"
	"testing"
)

func oneHead(seed int64) (*HeadManager, *SearchHead) {
	cfg := DefaultHeadManagerConfig()
	cfg.NumHeads, cfg.BaseSeed = 1, seed
	m := NewHeadManager(cfg)
	return m, m.GetHead(0)
}

func TestBeamBoundsCandidatesAndKeepsExploring(t *testing.T) {
	const count = 257
	for _, width := range []int{1, 4, 32, 512} {
		tree := NewArmTree(testPrefixes(count), DefaultTreeConfig())
		manager, head := oneHead(19)
		exposed := make(map[netip.Prefix]bool)
		for i := 0; i < count; i++ {
			selected := manager.SelectNextPrefix(head, tree, width)
			if !selected.IsValid() || len(head.candidates) > min(width, count) {
				t.Fatalf("width=%d selected=%s candidates=%d", width, selected, len(head.candidates))
			}
			inBeam := false
			seen := make(map[netip.Prefix]bool)
			for _, c := range head.candidates {
				if seen[c.node.Prefix] {
					t.Fatal("duplicate candidate occupied a beam slot")
				}
				seen[c.node.Prefix], exposed[c.node.Prefix] = true, true
				inBeam = inBeam || c.node.Prefix == selected
			}
			if !inBeam || len(head.candidateSet) != len(head.candidates) {
				t.Fatal("selection escaped its beam or left stale membership")
			}
			tree.Update(selected, true, 50, 3000)
		}
		if len(exposed) != count {
			t.Fatalf("width=%d only exposed %d/%d leaves", width, len(exposed), count)
		}
	}
}

func TestBeamOneVisitsEveryLeaf(t *testing.T) {
	prefixes := testPrefixes(60) // Non-prime frontier exercises coprime strides.
	tree := NewArmTree(prefixes, DefaultTreeConfig())
	m, h := oneHead(37)
	seen := make(map[netip.Prefix]bool)
	for range prefixes {
		p := m.SelectNextPrefix(h, tree, 1)
		if seen[p] || !p.IsValid() {
			t.Fatalf("beam 1 repeated before covering the frontier: %s", p)
		}
		seen[p] = true
	}
}

func TestBeamRemovesSplitAndExhaustedLeaves(t *testing.T) {
	prefix := netip.MustParsePrefix("192.0.2.0/24")
	cfg := DefaultTreeConfig()
	cfg.MaxBitsV4, cfg.MinSamples = 28, 1
	tree := NewArmTree([]netip.Prefix{prefix}, cfg)
	m, h := oneHead(19)
	if got := m.SelectNextPrefix(h, tree, 4); got != prefix {
		t.Fatal(got)
	}
	tree.Update(prefix, true, 20, 3000)
	children := tree.SplitNode(tree.GetNode(prefix))
	if got := m.SelectNextPrefix(h, tree, 4); got == prefix || !got.IsValid() {
		t.Fatalf("selected stale parent after split: %s", got)
	}
	for _, child := range children {
		tree.RetirePrefix(child.Prefix)
		got := m.SelectNextPrefix(h, tree, 4)
		if got == child.Prefix {
			t.Fatal("selected newly exhausted prefix")
		}
	}
	if got := m.SelectNextPrefix(h, tree, 4); got.IsValid() {
		t.Fatalf("exhausted tree selected %s", got)
	}
}

func TestBeamFindsAvailableLeafOutsideItsCache(t *testing.T) {
	prefixes := testPrefixes(32)
	tree := NewArmTree(prefixes, DefaultTreeConfig())
	m, h := oneHead(19)
	m.SelectNextPrefix(h, tree, 4)
	want := prefixes[0]
	for _, p := range prefixes {
		if _, cached := h.candidateSet[tree.GetNode(p)]; !cached {
			want = p
			break
		}
	}
	got := m.SelectNextAvailablePrefix(h, tree, 4, func(p netip.Prefix) bool { return p == want })
	if got != want {
		t.Fatalf("hidden available leaf: got %s want %s", got, want)
	}
	if got = m.SelectNextAvailablePrefix(h, tree, 4, func(netip.Prefix) bool { return false }); got.IsValid() {
		t.Fatal("unavailable frontier did not terminate")
	}
}

func TestSearchHeadHistoryIsBoundedAndOrdered(t *testing.T) {
	prefixes := testPrefixes(10)
	h := NewSearchHead(0, 1, 3000, 3)
	for _, p := range prefixes {
		h.SetFocus(p)
	}
	if got := h.GetHistory(); !reflect.DeepEqual(got, prefixes[7:]) || len(h.History) != 3 {
		t.Fatalf("history=%v", got)
	}
}
