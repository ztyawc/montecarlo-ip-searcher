package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const cloudflareAPIBase = "https://api.cloudflare.com/client/v4"

type CloudflareProvider struct {
	token    string
	zoneID   string
	zoneName string
	client   *http.Client
}

func NewCloudflareProvider(token, zoneID string) *CloudflareProvider {
	return &CloudflareProvider{token: token, zoneID: zoneID, client: newHTTPClient()}
}

func (p *CloudflareProvider) Name() string { return "cloudflare" }

type cfDNSRecord struct {
	Record
	Name string `json:"name"`
}

type cfResponse struct {
	Success bool `json:"success"`
	Errors  []struct {
		Message string `json:"message"`
	} `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo *struct {
		Page       int  `json:"page"`
		PerPage    int  `json:"per_page"`
		TotalCount *int `json:"total_count"`
		TotalPages int  `json:"total_pages"`
	} `json:"result_info"`
}

func (p *CloudflareProvider) request(ctx context.Context, method, path string, payload any) (cfResponse, error) {
	var result cfResponse
	if err := requestJSON(ctx, p.client, method, cloudflareAPIBase+path, p.token, payload, &result); err != nil {
		return result, err
	}
	if !result.Success || len(result.Errors) > 0 {
		if len(result.Errors) > 0 {
			return result, fmt.Errorf("cloudflare API error: %s", result.Errors[0].Message)
		}
		return result, fmt.Errorf("cloudflare API did not confirm success")
	}
	return result, nil
}

func (p *CloudflareProvider) recordsPath() string {
	return "/zones/" + url.PathEscape(p.zoneID) + "/dns_records"
}

func (p *CloudflareProvider) buildFQDN(ctx context.Context, subdomain string) (string, error) {
	name, err := normalizeSubdomain(subdomain)
	if err != nil {
		return "", err
	}
	if p.zoneName == "" {
		response, err := p.request(ctx, http.MethodGet, "/zones/"+url.PathEscape(p.zoneID), nil)
		if err != nil {
			return "", err
		}
		var zone struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(response.Result, &zone); err != nil {
			return "", fmt.Errorf("invalid Cloudflare zone response: %w", err)
		}
		zoneName, err := normalizeDomain(zone.Name)
		if err != nil {
			return "", fmt.Errorf("invalid Cloudflare zone name: %w", err)
		}
		p.zoneName = zoneName
	}
	if name == "" {
		return p.zoneName, nil
	}
	name += "." + p.zoneName
	if err := validateDNSName(name, true); err != nil {
		return "", err
	}
	return name, nil
}

// ListRecords uses numbered pagination and filters the returned records again,
// so an unexpected API filter result cannot widen the scope of a later delete.
func (p *CloudflareProvider) ListRecords(ctx context.Context, subdomain string) ([]Record, error) {
	fqdn, err := p.buildFQDN(ctx, subdomain)
	if err != nil {
		return nil, err
	}
	var records []Record
	seen := make(map[string]cfDNSRecord)
	var totalCount *int
	finish := func() ([]Record, error) {
		if totalCount != nil && len(seen) != *totalCount {
			return nil, fmt.Errorf("Cloudflare pagination returned %d of %d records", len(seen), *totalCount)
		}
		return checkedRecords(records)
	}
	for page := 1; page <= maxDNSPages; page++ {
		query := url.Values{
			"name.exact": {fqdn}, "match": {"all"},
			"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(dnsPageSize)},
		}
		response, err := p.request(ctx, http.MethodGet, p.recordsPath()+"?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		var rows []cfDNSRecord
		if err := json.Unmarshal(response.Result, &rows); err != nil || rows == nil {
			return nil, fmt.Errorf("invalid Cloudflare record list")
		}
		info := response.ResultInfo
		if info != nil && ((info.Page != 0 && info.Page != page) || info.TotalPages < 0 || info.PerPage < 0 || (info.PerPage > 0 && len(rows) > info.PerPage) || (info.TotalPages > 0 && page > info.TotalPages)) {
			return nil, fmt.Errorf("invalid Cloudflare pagination on page %d", page)
		}
		if info != nil && info.TotalCount != nil {
			if *info.TotalCount < 0 || (totalCount != nil && *totalCount != *info.TotalCount) {
				return nil, fmt.Errorf("Cloudflare record count changed during pagination")
			}
			totalCount = info.TotalCount
		}
		if len(rows) == 0 {
			if info != nil && info.TotalPages > page {
				return nil, fmt.Errorf("Cloudflare pagination ended before its last page")
			}
			return finish()
		}
		newIDs := 0
		for _, r := range rows {
			if r.ID == "" || r.Name == "" || r.Type == "" {
				return nil, fmt.Errorf("incomplete Cloudflare DNS record")
			}
			if previous, ok := seen[r.ID]; ok {
				if previous.Name != r.Name || !sameRecord(previous.Record, r.Record) {
					return nil, fmt.Errorf("Cloudflare record %s changed during pagination", r.ID)
				}
				continue
			}
			seen[r.ID] = r
			newIDs++
			if strings.EqualFold(strings.TrimSuffix(r.Name, "."), fqdn) && (r.Type == "A" || r.Type == "AAAA") {
				records = append(records, r.Record)
			}
		}
		if newIDs == 0 {
			return nil, fmt.Errorf("Cloudflare pagination did not advance")
		}
		if totalCount != nil && len(seen) > *totalCount {
			return nil, fmt.Errorf("Cloudflare pagination exceeded its declared record count")
		}
		if info != nil && info.TotalPages > 0 && page == info.TotalPages {
			return finish()
		}
		// Without total_pages, request the next page until an empty page proves
		// completion; never assume the API used our requested page size.
	}
	return nil, fmt.Errorf("Cloudflare pagination exceeded %d pages", maxDNSPages)
}

func (p *CloudflareProvider) CreateRecord(ctx context.Context, subdomain string, record Record) (Record, error) {
	if _, err := recordKey(record); err != nil {
		return Record{}, err
	}
	fqdn, err := p.buildFQDN(ctx, subdomain)
	if err != nil {
		return Record{}, err
	}
	if record.TTL == 0 {
		record.TTL = 1
	}
	payload := map[string]any{
		"name": fqdn, "type": record.Type, "content": record.Content,
		"ttl": record.TTL, "proxied": record.Proxied,
	}
	if record.Comment != "" {
		payload["comment"] = record.Comment
	}
	if len(record.Tags) > 0 {
		payload["tags"] = record.Tags
	}
	if len(record.Settings) > 0 {
		payload["settings"] = record.Settings
	}
	if record.PrivateRouting {
		payload["private_routing"] = true
	}
	response, err := p.request(ctx, http.MethodPost, p.recordsPath(), payload)
	if err != nil {
		return Record{}, err
	}
	var created cfDNSRecord
	if err := json.Unmarshal(response.Result, &created); err != nil {
		return Record{}, fmt.Errorf("invalid Cloudflare creation response: %w", err)
	}
	want, _ := recordKey(record)
	got, err := recordKey(created.Record)
	if err != nil || created.ID == "" || got != want || !strings.EqualFold(strings.TrimSuffix(created.Name, "."), fqdn) {
		return Record{}, fmt.Errorf("Cloudflare creation response did not identify the requested record")
	}
	return created.Record, nil
}

func (p *CloudflareProvider) DeleteRecord(ctx context.Context, recordID string) error {
	if recordID == "" {
		return fmt.Errorf("Cloudflare record ID must not be empty")
	}
	response, err := p.request(ctx, http.MethodDelete, p.recordsPath()+"/"+url.PathEscape(recordID), nil)
	if err != nil {
		return err
	}
	var deleted struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Result, &deleted); err != nil || deleted.ID != recordID {
		return fmt.Errorf("Cloudflare deletion response did not identify record %s", recordID)
	}
	return nil
}
