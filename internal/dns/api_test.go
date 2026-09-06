package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(r *http.Request, status int, value any) *http.Response {
	data, _ := json.Marshal(value)
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data))), Request: r}
}

func providerWithTransport(kind string, transport http.RoundTripper) Provider {
	if kind == "cloudflare" {
		p := NewCloudflareProvider("test-token", "test-zone")
		p.zoneName = "example.com"
		p.client.Transport = transport
		return p
	}
	p := NewVercelProvider("test-token", "example.com", "team&literal=1")
	p.client.Transport = transport
	return p
}

// apiFixture exercises the real adapters against a stateful, paginated API.
// It never dials: every HTTP operation is handled by the in-memory transport.
type apiFixture struct {
	t                           *testing.T
	kind                        string
	records                     map[string]cfDNSRecord
	creates, deletes, listCalls int
	failCreate, failDelete      int
	applyFailure                bool
}

func newAPIFixture(t *testing.T, kind string) (*apiFixture, Provider) {
	t.Helper()
	f := &apiFixture{t: t, kind: kind, records: make(map[string]cfDNSRecord)}
	for _, r := range []Record{oldRecord("old-a", "192.0.2.1"), oldRecord("old-b", "192.0.2.2")} {
		if kind == "vercel" {
			r.Proxied, r.PrivateRouting, r.Settings, r.Tags = false, false, nil, nil
		}
		f.records[r.ID] = cfDNSRecord{Record: r, Name: f.apiName("cf")}
	}
	f.records["unrelated-name"] = cfDNSRecord{Record: oldRecord("unrelated-name", "192.0.2.90"), Name: f.apiName("other")}
	f.records["txt"] = cfDNSRecord{Record: Record{ID: "txt", Type: "TXT", Content: "preserve=verification", TTL: 60}, Name: f.apiName("cf")}
	p := providerWithTransport(kind, testTransport(f.roundTrip))
	if cp, ok := p.(*CloudflareProvider); ok {
		cp.zoneName = "" // Also exercise zone discovery and its response validation.
	}
	return f, p
}

func (f *apiFixture) apiName(subdomain string) string {
	if f.kind == "cloudflare" {
		return subdomain + ".example.com"
	}
	return subdomain
}

func (f *apiFixture) snapshot() []Record {
	var rows []Record
	for _, r := range f.records {
		if r.Name == f.apiName("cf") && (r.Type == "A" || r.Type == "AAAA") {
			rows = append(rows, r.Record)
		}
	}
	return rows
}

func (f *apiFixture) roundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("Authorization") != "Bearer test-token" {
		f.t.Fatal("API token missing from an operation")
	}
	if f.kind == "vercel" && r.URL.Query().Get("teamId") != "team&literal=1" {
		f.t.Fatal("team scope was lost or incorrectly escaped")
	}
	if f.kind == "cloudflare" && r.URL.Path == "/client/v4/zones/test-zone" {
		return jsonResponse(r, 200, map[string]any{"success": true, "result": map[string]string{"name": "example.com"}}), nil
	}
	if r.Method == http.MethodGet {
		return f.list(r), nil
	}
	if r.Method == http.MethodPost {
		f.creates++
		failed := f.creates == f.failCreate
		if failed && !f.applyFailure {
			return jsonResponse(r, 429, map[string]any{"error": "rate limited"}), nil
		}
		var data struct {
			Record
			Name  string `json:"name"`
			Value string `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			f.t.Fatal(err)
		}
		if f.kind == "vercel" {
			data.Content = data.Value
		}
		data.ID = fmt.Sprintf("added-%d", f.creates)
		created := cfDNSRecord{Record: data.Record, Name: data.Name}
		f.records[data.ID] = created
		if failed {
			return nil, errInjected // Mutation applied, but no response arrived.
		}
		if f.kind == "cloudflare" {
			return jsonResponse(r, 200, map[string]any{"success": true, "result": created}), nil
		}
		return jsonResponse(r, 200, map[string]string{"uid": data.ID}), nil
	}
	if r.Method == http.MethodDelete {
		f.deletes++
		failed := f.deletes == f.failDelete
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if !failed || f.applyFailure {
			delete(f.records, id)
		}
		if failed {
			return jsonResponse(r, 503, map[string]any{"error": "temporary failure"}), nil
		}
		if f.kind == "cloudflare" {
			return jsonResponse(r, 200, map[string]any{"success": true, "result": map[string]string{"id": id}}), nil
		}
		return jsonResponse(r, 200, map[string]any{}), nil
	}
	f.t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
	return nil, nil
}

func (f *apiFixture) list(r *http.Request) *http.Response {
	f.listCalls++
	ids := make([]string, 0, len(f.records))
	for id := range f.records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	offset := 0
	if f.kind == "cloudflare" {
		if r.URL.Query().Get("name.exact") != "cf.example.com" || r.URL.Query().Get("match") != "all" {
			f.t.Fatal("Cloudflare name filter is not exact")
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		offset = page - 1
	} else {
		if r.URL.Path != "/v5/domains/example.com/records" {
			f.t.Fatalf("unexpected list endpoint %s", r.URL.Path)
		}
		if next := r.URL.Query().Get("until"); next != "" {
			n, _ := strconv.Atoi(next)
			offset = 1_000_000 - n
		}
	}
	// Deliberately return one record per page, even though the client requests
	// 100, and include unrelated names/types to test local scope checks.
	var cfRows = make([]cfDNSRecord, 0)
	var vercelRows = make([]any, 0)
	if offset >= 0 && offset < len(ids) {
		record := f.records[ids[offset]]
		cfRows = append(cfRows, record)
		vercelRows = append(vercelRows, map[string]any{
			"id": record.ID, "type": record.Type, "name": record.Name,
			"value": record.Content, "ttl": record.TTL, "comment": record.Comment,
		})
	}
	if f.kind == "cloudflare" {
		return jsonResponse(r, 200, map[string]any{
			"success": true, "result": cfRows,
			"result_info": map[string]int{"page": offset + 1, "per_page": 1, "total_pages": max(1, len(ids)), "total_count": len(ids)},
		})
	}
	var next any
	if offset+1 < len(ids) {
		next = 1_000_000 - offset - 1
	}
	return jsonResponse(r, 200, map[string]any{"records": vercelRows, "pagination": map[string]any{"next": next}})
}

func TestProviderPaginationAndRecoveryOverHTTP(t *testing.T) {
	for _, kind := range []string{"cloudflare", "vercel"} {
		for _, scenario := range []string{"success", "create fails", "second create fails", "create response lost", "delete fails", "delete applied then fails"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				api, provider := newAPIFixture(t, kind)
				before := api.snapshot()
				switch scenario {
				case "create fails":
					api.failCreate = 1
				case "second create fails":
					api.failCreate = 2
				case "create response lost":
					api.failCreate, api.applyFailure = 1, true
				case "delete fails":
					api.failDelete = 2
				case "delete applied then fails":
					api.failDelete, api.applyFailure = 2, true
				}
				err := Upload(context.Background(), provider, "cf", addresses("192.0.2.3", "192.0.2.4"), false)
				if scenario == "success" {
					if err != nil || api.creates != 2 || api.deletes != 2 {
						t.Fatalf("err=%v creates=%d deletes=%d", err, api.creates, api.deletes)
					}
					if api.listCalls < 3*4 {
						t.Fatal("upload did not fully paginate each snapshot")
					}
				} else {
					if err == nil {
						t.Fatal("API failure was reported as success")
					}
					assertPreserved(t, api.snapshot(), before)
					if strings.Contains(scenario, "create") && api.deletes != 0 {
						t.Fatal("creation failure deleted old records")
					}
				}
				if _, ok := api.records["unrelated-name"]; !ok {
					t.Fatal("another subdomain was modified")
				}
				if _, ok := api.records["txt"]; !ok {
					t.Fatal("TXT record was modified")
				}
			})
		}
	}
}

func TestCloudflareRejectsIncompletePaginationBeforeWrites(t *testing.T) {
	row := cfDNSRecord{Record: Record{ID: "old", Type: "A", Content: "192.0.2.1", TTL: 60}, Name: "cf.example.com"}
	for _, tc := range []struct {
		name   string
		bodies []any
	}{
		{"empty page claims records", []any{map[string]any{"success": true, "result": []any{}, "result_info": map[string]int{"page": 1, "total_pages": 1, "total_count": 1}}}},
		{"last page count mismatch", []any{map[string]any{"success": true, "result": []any{row}, "result_info": map[string]int{"page": 1, "total_pages": 1, "total_count": 2}}}},
		{"explicit zero count with record", []any{map[string]any{"success": true, "result": []any{row}, "result_info": map[string]int{"page": 1, "total_pages": 1, "total_count": 0}}}},
		{"repeated page", []any{
			map[string]any{"success": true, "result": []any{row}, "result_info": map[string]int{"page": 1, "total_pages": 2, "total_count": 2}},
			map[string]any{"success": true, "result": []any{row}, "result_info": map[string]int{"page": 2, "total_pages": 2, "total_count": 2}},
		}},
		{"second page fails", []any{map[string]any{"success": true, "result": []any{row}, "result_info": map[string]int{"page": 1, "total_pages": 2, "total_count": 2}}}},
		{"missing result", []any{map[string]any{"success": true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			p := providerWithTransport("cloudflare", testTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet {
					t.Fatal("incomplete snapshot caused a write")
				}
				calls++
				if calls > len(tc.bodies) {
					return jsonResponse(r, 503, map[string]any{}), nil
				}
				return jsonResponse(r, 200, tc.bodies[calls-1]), nil
			}))
			if err := Upload(context.Background(), p, "cf", addresses("192.0.2.2"), false); err == nil {
				t.Fatal("incomplete snapshot was accepted")
			}
		})
	}
}

func TestCloudflarePaginationWithoutTotalPages(t *testing.T) {
	calls := 0
	p := providerWithTransport("cloudflare", testTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		rows := []cfDNSRecord{}
		if calls == 1 {
			rows = append(rows, cfDNSRecord{Record: Record{ID: "old", Type: "A", Content: "192.0.2.1", TTL: 60}, Name: "cf.example.com"})
		}
		return jsonResponse(r, 200, map[string]any{"success": true, "result": rows, "result_info": map[string]int{"page": calls, "total_count": 1}}), nil
	}))
	rows, err := p.ListRecords(context.Background(), "cf")
	if err != nil || calls != 2 || len(rows) != 1 {
		t.Fatalf("incomplete fallback pagination: rows=%v err=%v calls=%d", rows, err, calls)
	}
}

func TestVercelRejectsInvalidPaginationBeforeWrites(t *testing.T) {
	row := map[string]any{"id": "old", "name": "cf", "type": "A", "value": "192.0.2.1", "ttl": 60}
	full := make([]any, dnsPageSize)
	for i := range full {
		full[i] = map[string]any{"id": fmt.Sprint(i), "name": "cf", "type": "A", "value": "192.0.2.1", "ttl": 60}
	}
	for _, tc := range []struct {
		name   string
		bodies []any
	}{
		{"missing records", []any{map[string]any{"pagination": map[string]any{"next": nil}}}},
		{"missing cursor", []any{map[string]any{"records": []any{row}, "pagination": map[string]any{}}}},
		{"string cursor", []any{map[string]any{"records": []any{row}, "pagination": map[string]any{"next": "123"}}}},
		{"empty page with next", []any{map[string]any{"records": []any{}, "pagination": map[string]any{"next": 123}}}},
		{"repeated cursor", []any{
			map[string]any{"records": []any{row}, "pagination": map[string]any{"next": 123}},
			map[string]any{"records": []any{row}, "pagination": map[string]any{"next": 123}},
		}},
		{"full page without pagination", []any{map[string]any{"records": full}}},
		{"second page fails", []any{map[string]any{"records": []any{row}, "pagination": map[string]any{"next": 123}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			p := providerWithTransport("vercel", testTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet {
					t.Fatal("incomplete Vercel snapshot caused a write")
				}
				calls++
				if calls > len(tc.bodies) {
					return jsonResponse(r, 503, map[string]any{}), nil
				}
				return jsonResponse(r, 200, tc.bodies[calls-1]), nil
			}))
			if err := Upload(context.Background(), p, "cf", addresses("192.0.2.2"), false); err == nil {
				t.Fatal("invalid pagination was accepted")
			}
		})
	}
}

func TestProvidersRejectInvalidMutationResponses(t *testing.T) {
	for _, tc := range []struct {
		kind, method string
		status       int
		body         any
	}{
		{"cloudflare", "POST", 200, map[string]any{"success": true, "result": map[string]any{}}},
		{"cloudflare", "POST", 200, map[string]any{"success": false}},
		{"cloudflare", "POST", 500, map[string]any{"success": true}},
		{"cloudflare", "DELETE", 200, map[string]any{"success": true, "result": map[string]string{"id": "different"}}},
		{"vercel", "POST", 200, map[string]any{}},
		{"vercel", "POST", 200, map[string]any{"uid": "new", "error": map[string]string{"message": "failure"}}},
		{"vercel", "POST", 500, map[string]any{"uid": "new"}},
		{"vercel", "DELETE", 200, map[string]any{"error": map[string]string{"message": "failure"}}},
		{"vercel", "DELETE", 500, map[string]any{}},
	} {
		p := providerWithTransport(tc.kind, testTransport(func(r *http.Request) (*http.Response, error) {
			return jsonResponse(r, tc.status, tc.body), nil
		}))
		var err error
		if tc.method == "POST" {
			_, err = p.CreateRecord(context.Background(), "cf", Record{Type: "A", Content: "192.0.2.1"})
		} else {
			err = p.DeleteRecord(context.Background(), "old")
		}
		if err == nil {
			t.Fatalf("invalid response accepted: %s %s %+v", tc.kind, tc.method, tc.body)
		}
	}
}

func TestVercelApexUsesEmptyName(t *testing.T) {
	name := "unset"
	p := providerWithTransport("vercel", testTransport(func(r *http.Request) (*http.Response, error) {
		var payload struct{ Name string }
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		name = payload.Name
		return jsonResponse(r, 200, map[string]string{"uid": "apex"}), nil
	}))
	if _, err := p.CreateRecord(context.Background(), "@", Record{Type: "A", Content: "192.0.2.1"}); err != nil || name != "" {
		t.Fatalf("apex name=%q err=%v", name, err)
	}
}
