package bandit

import (
	"net/netip"
	"reflect"
	"sync"
	"testing"
)

func testPrefixes(n int) []netip.Prefix {
	result := make([]netip.Prefix, n)
	for i := range result {
		result[i] = netip.PrefixFrom(netip.AddrFrom4([4]byte{10, byte(i >> 8), byte(i), 0}), 24)
	}
	return result
}

func TestSplitRespectsAddressAndResourceBounds(t *testing.T) {
	for _, tc := range []struct {
		name, prefix            string
		step, maxBits, maxNodes int
		wantChildren, wantBits  int
	}{
		{"IPv4 boundary", "192.0.2.0/23", 2, 24, 0, 2, 24},
		{"IPv6 boundary", "2001:db8::/54", 4, 56, 0, 4, 56},
		{"last IPv4 bit", "192.0.2.0/31", 2, 32, 0, 2, 32},
		{"last IPv6 bit", "2001:db8::/127", 4, 128, 0, 2, 128},
		{"IPv4 at limit", "192.0.2.0/24", 2, 24, 0, 0, 0},
		{"IPv6 large step", "2001:db8::/32", 16, 64, 0, 256, 40},
		{"IPv6 clipped step", "2001:db8::/48", 16, 56, 0, 256, 56},
		{"capacity clips step", "2001:db8::/32", 16, 64, 5, 4, 34},
		{"capacity cannot fit split", "192.0.2.0/24", 2, 32, 2, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultTreeConfig()
			cfg.SplitStepV4, cfg.SplitStepV6 = tc.step, tc.step
			cfg.MaxBitsV4, cfg.MaxBitsV6 = tc.maxBits, tc.maxBits
			cfg.MaxNodes, cfg.MinSamples = tc.maxNodes, 1
			prefix := netip.MustParsePrefix(tc.prefix)
			tree := NewArmTree([]netip.Prefix{prefix}, cfg)
			node := tree.GetNode(prefix)
			node.Update(true, 20, 3000)
			children := tree.SplitNode(node)
			if len(children) != tc.wantChildren || len(children) > MaxChildrenPerSplit {
				t.Fatalf("got %d children, want %d", len(children), tc.wantChildren)
			}
			if len(children) == 0 {
				if node.Stats().IsSplit || tree.LeafCount() != 1 {
					t.Fatal("unsplittable parent disappeared from search")
				}
				return
			}
			seen := make(map[netip.Prefix]bool)
			for _, child := range children {
				if child.Prefix.Bits() != tc.wantBits || !prefix.Contains(child.Prefix.Addr()) || seen[child.Prefix] {
					t.Fatalf("invalid or repeated child: %s", child.Prefix)
				}
				seen[child.Prefix] = true
				if child.Stats().Samples != 0 {
					t.Fatal("child inherited and double-counted parent observations")
				}
			}
			if len(children) != 1<<(tc.wantBits-prefix.Bits()) || tree.Size() != 1+len(children) || tree.LeafCount() != len(children) {
				t.Fatal("split did not preserve the parent's complete address space")
			}
			if tc.maxNodes > 0 && tree.Size() > tc.maxNodes {
				t.Fatal("split exceeded the node budget")
			}
		})
	}
}

func TestTreeCapacityRetainsRootsAndSkipsSplitSearch(t *testing.T) {
	cfg := DefaultTreeConfig()
	cfg.MaxNodes, cfg.MinSamples, cfg.MaxBitsV4 = 3, 1, 32
	prefixes := testPrefixes(5)
	tree := NewArmTree(prefixes, cfg)
	for _, p := range prefixes {
		tree.Update(p, true, 10, 3000)
	}
	if tree.Size() != 5 || tree.LeafCount() != 5 || tree.CanExpand() {
		t.Fatal("capacity discarded roots or allowed extra nodes")
	}
	if got := tree.GetSplitCandidates(5); len(got) != 0 {
		t.Fatalf("full tree proposed splits: %v", got)
	}
	allocs := testing.AllocsPerRun(20, func() { tree.GetSplitCandidates(5) })
	if allocs != 0 {
		t.Fatalf("full tree still allocates split candidates: %g", allocs)
	}
}

func TestTreeOrderAndRetirement(t *testing.T) {
	prefixes := testPrefixes(4)
	input := []netip.Prefix{prefixes[3], prefixes[0], prefixes[2], prefixes[1], prefixes[0]}
	tree := NewArmTree(input, DefaultTreeConfig())
	var got []netip.Prefix
	for _, node := range tree.LeafNodes() {
		got = append(got, node.Prefix)
	}
	if !reflect.DeepEqual(got, prefixes) {
		t.Fatalf("unstable root order: %v", got)
	}
	tree.Update(prefixes[1], true, 10, 3000)
	tree.RetirePrefix(prefixes[1])
	if tree.LeafCount() != 3 || tree.Size() != 4 || tree.GetNode(prefixes[1]).Stats().Samples != 1 {
		t.Fatal("retirement lost statistics or retained a selectable leaf")
	}
	for _, node := range tree.LeafNodes() {
		if node.Prefix == prefixes[1] {
			t.Fatal("retired prefix remains selectable")
		}
	}
	snapshot := tree.AllNodes()
	snapshot[0] = nil
	if tree.AllNodes()[0] == nil {
		t.Fatal("snapshot aliases the node index")
	}
}

func TestConcurrentSelectionAndTreeChanges(t *testing.T) {
	prefixes := testPrefixes(16)
	cfg := DefaultTreeConfig()
	cfg.MaxBitsV4, cfg.MinSamples = 28, 1
	tree := NewArmTree(prefixes, cfg)
	hm := NewHeadManager(DefaultHeadManagerConfig())
	var wg sync.WaitGroup
	for i := 0; i < hm.NumHeads(); i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 150; j++ {
				p := hm.SelectNextPrefix(hm.GetHead(id), tree, 8)
				if p.IsValid() {
					tree.Update(p, true, 20, 3000)
				}
			}
		}(i)
	}
	for _, p := range prefixes {
		tree.Update(p, true, 20, 3000)
		children := tree.SplitNode(tree.GetNode(p))
		if len(children) > 0 {
			tree.RetirePrefix(children[0].Prefix)
		}
		hm.RebalanceHeads(tree)
	}
	wg.Wait()
}
