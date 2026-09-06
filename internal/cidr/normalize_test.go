package cidr

import (
	"net/netip"
	"reflect"
	"testing"
)

func TestRemoveContained(t *testing.T) {
	input, _ := ParseCIDRs([]string{"192.0.2.4/30", "192.0.2.1/32", "192.0.2.0/30", "192.0.2.0/31", "192.0.2.0/30", "2001:db8::1/128", "2001:db8::/126"})
	want, _ := ParseCIDRs([]string{"192.0.2.0/30", "192.0.2.4/30", "2001:db8::/126"})
	got := RemoveContained(input)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for _, p := range input {
		if !p.IsValid() {
			t.Fatal("modified input")
		}
	}
}

func TestRemoveContainedIPv4AndMappedIPv6RemainDistinct(t *testing.T) {
	input := []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::ffff:0:0/96"), netip.MustParsePrefix("::/0")}
	got := RemoveContained(input)
	if len(got) != 2 || got[0].String() != "0.0.0.0/0" || got[1].String() != "::/0" {
		t.Fatalf("got %v", got)
	}
}
