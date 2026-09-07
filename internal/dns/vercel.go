package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

const vercelAPIBase = "https://api.vercel.com"

type VercelProvider struct {
	token  string
	domain string
	teamID string
	client *http.Client
}

func NewVercelProvider(token, domain, teamID string) *VercelProvider {
	return &VercelProvider{token: token, domain: domain, teamID: teamID, client: newHTTPClient()}
}

func (p *VercelProvider) Name() string { return "vercel" }

type vercelDNSRecord struct {
	ID      string  `json:"id"`
	Type    string  `json:"type"`
	Name    *string `json:"name"` // Empty is the apex; absent is an invalid response.
	Value   string  `json:"value"`
	TTL     int     `json:"ttl"`
	Comment string  `json:"comment"`
}

func (r vercelDNSRecord) record() Record {
	return Record{ID: r.ID, Type: r.Type, Content: r.Value, TTL: r.TTL, Comment: r.Comment}
}

func (p *VercelProvider) requestURL(version, recordID string, query url.Values) (string, error) {
	domain, err := normalizeDomain(p.domain)
	if err != nil {
		return "", fmt.Errorf("invalid Vercel domain: %w", err)
	}
	path := vercelAPIBase + "/" + version + "/domains/" + url.PathEscape(domain) + "/records"
	if recordID != "" {
		path += "/" + url.PathEscape(recordID)
	}
	if query == nil {
		query = make(url.Values)
	}
	if p.teamID != "" {
		query.Set("teamId", p.teamID)
	}
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	return path, nil
}

func (p *VercelProvider) ListRecords(ctx context.Context, subdomain string) ([]Record, error) {
	name, err := normalizeSubdomain(subdomain)
	if err != nil {
		return nil, err
	}
	var records []Record
	seen := make(map[string]vercelDNSRecord)
	cursors := make(map[int64]bool)
	query := url.Values{"limit": {strconv.Itoa(dnsPageSize)}}
	for page := 0; page < maxDNSPages; page++ {
		requestURL, err := p.requestURL("v5", "", query)
		if err != nil {
			return nil, err
		}
		var result struct {
			Error      json.RawMessage `json:"error"`
			Records    json.RawMessage `json:"records"`
			Pagination *struct {
				Next json.RawMessage `json:"next"`
			} `json:"pagination"`
		}
		if err := requestJSON(ctx, p.client, http.MethodGet, requestURL, p.token, nil, &result); err != nil {
			return nil, err
		}
		if len(result.Error) > 0 {
			return nil, fmt.Errorf("Vercel record list contains an error")
		}
		var rows []vercelDNSRecord
		if err := json.Unmarshal(result.Records, &rows); err != nil || rows == nil {
			return nil, fmt.Errorf("invalid Vercel record list")
		}
		newIDs := 0
		for _, r := range rows {
			if r.ID == "" || r.Type == "" || r.Name == nil {
				return nil, fmt.Errorf("incomplete Vercel DNS record")
			}
			if previous, ok := seen[r.ID]; ok {
				if *previous.Name != *r.Name || !sameRecord(previous.record(), r.record()) {
					return nil, fmt.Errorf("Vercel record %s changed during pagination", r.ID)
				}
				continue
			}
			seen[r.ID] = r
			newIDs++
			recordName, err := normalizeSubdomain(*r.Name)
			if err == nil && recordName == name && (r.Type == "A" || r.Type == "AAAA") {
				records = append(records, r.record())
			}
		}
		if result.Pagination == nil {
			// The documented unpaginated response is only safe with fewer than
			// the explicitly requested limit; a full page needs a next cursor.
			if len(rows) >= dnsPageSize {
				return nil, fmt.Errorf("Vercel full page is missing pagination")
			}
			return checkedRecords(records)
		}
		var next *int64
		if err := json.Unmarshal(result.Pagination.Next, &next); err != nil {
			return nil, fmt.Errorf("invalid Vercel pagination cursor: %w", err)
		}
		if next == nil {
			return checkedRecords(records)
		}
		if *next < 0 || cursors[*next] || newIDs == 0 {
			return nil, fmt.Errorf("Vercel pagination did not advance")
		}
		cursors[*next] = true
		query.Set("until", strconv.FormatInt(*next, 10))
	}
	return nil, fmt.Errorf("Vercel pagination exceeded %d pages", maxDNSPages)
}

func (p *VercelProvider) CreateRecord(ctx context.Context, subdomain string, record Record) (Record, error) {
	if _, err := recordKey(record); err != nil {
		return Record{}, err
	}
	name, err := normalizeSubdomain(subdomain)
	if err != nil {
		return Record{}, err
	}
	requestURL, err := p.requestURL("v2", "", nil)
	if err != nil {
		return Record{}, err
	}
	if record.TTL == 0 {
		record.TTL = 60
	}
	payload := map[string]any{
		"name": name, "type": record.Type, "value": record.Content, "ttl": record.TTL,
	}
	if record.Comment != "" {
		payload["comment"] = record.Comment
	}
	var result struct {
		UID   string          `json:"uid"`
		Error json.RawMessage `json:"error"`
	}
	if err := requestJSON(ctx, p.client, http.MethodPost, requestURL, p.token, payload, &result); err != nil {
		return Record{}, err
	}
	if result.UID == "" || len(result.Error) > 0 {
		return Record{}, fmt.Errorf("Vercel creation response did not identify the new record")
	}
	record.ID = result.UID
	return record, nil
}

func (p *VercelProvider) DeleteRecord(ctx context.Context, recordID string) error {
	if recordID == "" {
		return fmt.Errorf("Vercel record ID must not be empty")
	}
	requestURL, err := p.requestURL("v2", recordID, nil)
	if err != nil {
		return err
	}
	var result map[string]json.RawMessage
	if err := requestJSON(ctx, p.client, http.MethodDelete, requestURL, p.token, nil, &result); err != nil {
		return err
	}
	if _, failed := result["error"]; failed {
		return fmt.Errorf("Vercel deletion response contains an error")
	}
	return nil
}
