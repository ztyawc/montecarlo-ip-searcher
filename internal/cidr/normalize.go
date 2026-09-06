package cidr

import (
	"net/netip"
	"sort"
)

// RemoveContained preserves the address union but removes duplicates and
// contained roots. Adjacent roots are deliberately not merged, preserving
// their original granularity for the search algorithm.
func RemoveContained(prefixes []netip.Prefix) []netip.Prefix {
	sorted := make([]netip.Prefix, 0, len(prefixes))
	for _, p := range prefixes {
		if p.IsValid() {
			sorted = append(sorted, p.Masked())
		}
	}
	sort.Slice(sorted, func(i, j int) bool {
		if cmp := sorted[i].Addr().Compare(sorted[j].Addr()); cmp != 0 {
			return cmp < 0
		}
		return sorted[i].Bits() < sorted[j].Bits()
	})
	out := make([]netip.Prefix, 0, len(sorted))
	for _, p := range sorted {
		if len(out) > 0 {
			last := out[len(out)-1]
			if last.Addr().BitLen() == p.Addr().BitLen() && last.Bits() <= p.Bits() && last.Contains(p.Addr()) {
				continue
			}
		}
		out = append(out, p)
	}
	return out
}
