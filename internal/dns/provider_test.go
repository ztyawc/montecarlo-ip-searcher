package dns

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"
)

var errInjected = errors.New("injected API failure")

type memoryProvider struct {
	records                    []Record
	lists, creates             int
	deleted                    []string
	createFailure, listFailure int
	deleteFailure              int
	applyFailedCreate          bool
	applyFailedDelete          bool
	listHook                   func(context.Context, int, []Record) ([]Record, error)
	deleteHook                 func(context.Context, int) error
}

func (*memoryProvider) Name() string { return "memory" }

func (p *memoryProvider) ListRecords(ctx context.Context, _ string) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.lists++
	if p.lists == p.listFailure {
		return nil, errInjected
	}
	rows := append([]Record(nil), p.records...)
	if p.listHook != nil {
		return p.listHook(ctx, p.lists, rows)
	}
	return rows, nil
}

func (p *memoryProvider) CreateRecord(ctx context.Context, _ string, r Record) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	p.creates++
	failed := p.creates == p.createFailure
	if !failed || p.applyFailedCreate {
		r.ID = fmt.Sprintf("created-%d", p.creates)
		if r.TTL == 0 {
			r.TTL = 60
		}
		p.records = append(p.records, r)
	}
	if failed {
		return Record{}, errInjected
	}
	return r, nil
}

func (p *memoryProvider) DeleteRecord(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.deleted = append(p.deleted, id)
	if p.deleteHook != nil {
		if err := p.deleteHook(ctx, len(p.deleted)); err != nil {
			return err
		}
	}
	failed := len(p.deleted) == p.deleteFailure
	if !failed || p.applyFailedDelete {
		for i, r := range p.records {
			if r.ID == id {
				p.records = append(p.records[:i], p.records[i+1:]...)
				break
			}
		}
	}
	if failed {
		return errInjected
	}
	return nil
}

func oldRecord(id, content string) Record {
	typ := "A"
	if netip.MustParseAddr(content).Is6() {
		typ = "AAAA"
	}
	return Record{ID: id, Type: typ, Content: content, TTL: 600, Proxied: true,
		Comment: "original comment", Tags: []string{"b:2", "a:1"},
		Settings: []byte(`{"ipv4_only":true}`), PrivateRouting: true}
}

func addresses(values ...string) []netip.Addr {
	ips := make([]netip.Addr, len(values))
	for i, value := range values {
		ips[i] = netip.MustParseAddr(value)
	}
	return ips
}

func assertPreserved(t *testing.T, records, want []Record) {
	t.Helper()
	for _, old := range want {
		found := false
		for _, got := range records {
			if sameRecord(got, old) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("old record properties were lost: %+v; current=%+v", old, records)
		}
	}
}

func TestUploadDiffAndIdempotence(t *testing.T) {
	for _, ipv6 := range []bool{false, true} {
		t.Run(fmt.Sprint(ipv6), func(t *testing.T) {
			keepIP, removeIP, newIP, otherIP := "192.0.2.1", "192.0.2.2", "192.0.2.3", "2001:db8::1"
			if ipv6 {
				keepIP, removeIP, newIP, otherIP = "2001:db8::1", "2001:db8::2", "2001:db8::3", "192.0.2.1"
			}
			keep, other := oldRecord("keep", keepIP), oldRecord("other-family", otherIP)
			p := &memoryProvider{records: []Record{keep, oldRecord("remove", removeIP), other}}
			ips := addresses(keepIP, newIP, newIP)
			if err := Upload(context.Background(), p, "cf", ips, false); err != nil {
				t.Fatal(err)
			}
			if p.creates != 1 || len(p.deleted) != 1 || p.deleted[0] != "remove" || len(p.records) != 3 {
				t.Fatalf("unexpected diff: creates=%d deleted=%v records=%+v", p.creates, p.deleted, p.records)
			}
			assertPreserved(t, p.records, []Record{keep, other})
			if err := Upload(context.Background(), p, "cf", ips, false); err != nil {
				t.Fatal(err)
			}
			if p.creates != 1 || len(p.deleted) != 1 {
				t.Fatal("identical upload performed additional writes")
			}
		})
	}
}

func TestUploadFailureBeforeDeleteRetainsOldRecords(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*memoryProvider)
	}{
		{"snapshot failure", func(p *memoryProvider) { p.listFailure = 1 }},
		{"first create fails", func(p *memoryProvider) { p.createFailure = 1 }},
		{"second create fails", func(p *memoryProvider) { p.createFailure = 2 }},
		{"create applied but response fails", func(p *memoryProvider) { p.createFailure, p.applyFailedCreate = 1, true }},
		{"confirmation fails", func(p *memoryProvider) { p.listFailure = 2 }},
		{"new record not visible", func(p *memoryProvider) {
			p.listHook = func(_ context.Context, n int, rows []Record) ([]Record, error) {
				if n == 2 {
					return rows[:1], nil
				}
				return rows, nil
			}
		}},
		{"old record changed", func(p *memoryProvider) {
			p.listHook = func(_ context.Context, n int, rows []Record) ([]Record, error) {
				if n == 2 {
					rows[0].TTL++
				}
				return rows, nil
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := oldRecord("old", "192.0.2.1")
			p := &memoryProvider{records: []Record{old}}
			tc.configure(p)
			err := Upload(context.Background(), p, "cf", addresses("192.0.2.2", "192.0.2.3"), false)
			if err == nil || len(p.deleted) != 0 {
				t.Fatalf("err=%v deleted=%v", err, p.deleted)
			}
			assertPreserved(t, p.records, []Record{old})
		})
	}
}

func TestUploadDeletionFailureRestoresProperties(t *testing.T) {
	for _, tc := range []struct {
		name          string
		deleteFailure int
		apply         bool
		listFailure   int
	}{
		{"first deletion rejected", 1, false, 0},
		{"partial deletion rejected", 2, false, 0},
		{"partial deletion applied but response failed", 2, true, 0},
		{"final verification failed", 0, false, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := []Record{oldRecord("old-a", "192.0.2.1"), oldRecord("old-b", "192.0.2.2")}
			p := &memoryProvider{records: append([]Record(nil), old...), deleteFailure: tc.deleteFailure,
				applyFailedDelete: tc.apply, listFailure: tc.listFailure}
			err := Upload(context.Background(), p, "cf", addresses("192.0.2.3"), false)
			if !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "original records restored") {
				t.Fatalf("unexpected failure: %v", err)
			}
			assertPreserved(t, p.records, old)
			_, present, _ := indexRecords(p.records)
			if !present["A/192.0.2.3"] {
				t.Fatal("recovery deleted the replacement record")
			}
		})
	}
}

func TestUploadReportsIncompleteRecovery(t *testing.T) {
	p := &memoryProvider{
		records:       []Record{oldRecord("old-a", "192.0.2.1"), oldRecord("old-b", "192.0.2.2")},
		deleteFailure: 2, applyFailedDelete: true, createFailure: 2,
	}
	err := Upload(context.Background(), p, "cf", addresses("192.0.2.3"), false)
	if err == nil || !strings.Contains(err.Error(), "recovery incomplete") {
		t.Fatalf("unexpected error: %v", err)
	}
	_, present, _ := indexRecords(p.records)
	if !present["A/192.0.2.3"] || !present["A/192.0.2.2"] {
		t.Fatal("recovery removed the replacement or stopped before restoring another record")
	}
}

func TestUploadCancellationStopsRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &memoryProvider{records: []Record{oldRecord("old-a", "192.0.2.1"), oldRecord("old-b", "192.0.2.2")}}
	p.deleteHook = func(_ context.Context, n int) error {
		if n == 2 {
			cancel()
			return context.Canceled
		}
		return nil
	}
	err := Upload(ctx, p, "cf", addresses("192.0.2.3"), false)
	if !errors.Is(err, context.Canceled) || p.lists != 2 || p.creates != 1 || len(p.deleted) != 2 {
		t.Fatalf("cancellation started more operations: err=%v lists=%d creates=%d deletes=%d", err, p.lists, p.creates, len(p.deleted))
	}
}

func TestUploadDeadlinesBoundWorkAndRecovery(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		t.Run(fmt.Sprint(recovery), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			p := &memoryProvider{records: []Record{oldRecord("old", "192.0.2.1")}}
			blockAt := 2
			if recovery {
				p.deleteFailure = 1
				blockAt = 3
			}
			p.listHook = func(ctx context.Context, n int, rows []Record) ([]Record, error) {
				if n == blockAt {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return rows, nil
			}
			start := time.Now()
			err := Upload(ctx, p, "cf", addresses("192.0.2.2"), false)
			if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
				t.Fatalf("upload exceeded its bound or lost deadline cause: %v", err)
			}
		})
	}
	p := &memoryProvider{}
	p.listHook = func(ctx context.Context, _ int, rows []Record) ([]Record, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > DefaultUploadTimeout || time.Until(deadline) < 40*time.Second {
			t.Fatalf("missing default operation deadline: %v %v", deadline, ok)
		}
		return nil, errInjected
	}
	_ = Upload(context.Background(), p, "cf", addresses("192.0.2.1"), false)
}

func TestUploadRejectsInvalidInputBeforeRequests(t *testing.T) {
	for _, tc := range []struct {
		name string
		ips  []netip.Addr
	}{
		{"cf", []netip.Addr{{}}},
		{"cf", addresses("fe80::1%eth0")},
		{"cf&match=any", addresses("192.0.2.1")},
		{"https://example.com", addresses("192.0.2.1")},
	} {
		p := &memoryProvider{}
		if err := Upload(context.Background(), p, tc.name, tc.ips, false); err == nil || p.lists != 0 {
			t.Fatalf("invalid input made a request: %s %+v", tc.name, tc.ips)
		}
	}
	p := &memoryProvider{}
	if err := Upload(context.Background(), p, "cf", nil, false); err != nil || p.lists != 0 {
		t.Fatal("empty upload should be a no-op")
	}
}

func TestUploadRejectsInvalidSnapshot(t *testing.T) {
	for _, change := range []func(*Record){
		func(r *Record) { r.ID = "" },
		func(r *Record) { r.Content = "not-an-IP" },
		func(r *Record) { r.TTL = 0 },
		func(r *Record) { r.Type = "AAAA" },
		func(r *Record) { r.Settings = []byte(`{broken`) },
	} {
		r := oldRecord("old", "192.0.2.1")
		change(&r)
		p := &memoryProvider{records: []Record{r}}
		if err := Upload(context.Background(), p, "cf", addresses("192.0.2.2"), false); err == nil || p.creates != 0 || len(p.deleted) != 0 {
			t.Fatalf("invalid snapshot caused writes: %+v", r)
		}
	}
}

func TestUploadDetectsConcurrentExtraRecordBeforeDeleting(t *testing.T) {
	for _, hasOld := range []bool{false, true} {
		p := &memoryProvider{}
		if hasOld {
			p.records = []Record{oldRecord("old", "192.0.2.1")}
		}
		p.listHook = func(_ context.Context, n int, rows []Record) ([]Record, error) {
			if n == 2 {
				rows = append(rows, oldRecord("external", "192.0.2.99"))
			}
			return rows, nil
		}
		err := Upload(context.Background(), p, "cf", addresses("192.0.2.2"), false)
		if err == nil || len(p.deleted) != 0 {
			t.Fatalf("concurrent extra record was missed: old=%v err=%v deletes=%v", hasOld, err, p.deleted)
		}
	}
}

func TestRecordComparisonUsesSemanticValues(t *testing.T) {
	a := oldRecord("first", "2001:db8::1")
	a.Settings = []byte(`{"ipv4_only":false,"ipv6_only":true}`)
	b := a
	b.ID = "restored"
	b.Content = "2001:0db8:0000:0000:0000:0000:0000:0001"
	b.Settings = []byte(`{ "ipv6_only": true, "ipv4_only": false }`)
	b.Tags = []string{"a:1", "b:2"}
	if !sameRecord(a, b) {
		t.Fatal("equivalent IP/JSON/tag representations were treated as a conflict")
	}
	b.Settings = []byte(`{"ipv4_only":true,"ipv6_only":true}`)
	if sameRecord(a, b) {
		t.Fatal("different DNS settings were ignored")
	}
	a.Settings, b.Settings = nil, []byte(`{}`)
	if !sameRecord(a, b) {
		t.Fatal("absent and empty settings should compare equally")
	}
}
