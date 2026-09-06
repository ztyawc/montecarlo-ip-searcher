package bandit

import (
	"fmt"
	"testing"
)

// Compare candidate widths at a fixed number of live leaves. This measures the
// selection component only, not HTTP throughput or end-to-end scan quality.
func BenchmarkBeamSelection(b *testing.B) {
	for _, leaves := range []int{256, 4096, 65536} {
		for _, width := range []int{1, 32, 256} {
			b.Run(fmt.Sprintf("leaves=%d/beam=%d", leaves, width), func(b *testing.B) {
				tree := NewArmTree(testPrefixes(leaves), DefaultTreeConfig())
				manager, head := oneHead(19)
				manager.SelectNextPrefix(head, tree, width)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					manager.SelectNextPrefix(head, tree, width)
				}
			})
		}
	}
}

func BenchmarkSplitAtCapacity(b *testing.B) {
	for _, observed := range []bool{false, true} {
		b.Run(fmt.Sprintf("observed=%v", observed), func(b *testing.B) {
			cfg := DefaultTreeConfig()
			cfg.MaxBitsV4 = 32
			tree := NewArmTree(testPrefixes(DefaultMaxTreeNodes), cfg)
			if observed {
				for _, node := range tree.AllNodes() {
					for j := 0; j < cfg.MinSamples; j++ {
						node.Update(true, 50, 3000)
					}
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tree.GetSplitCandidates(16)
			}
		})
	}
}
