package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	requestTimeout   = 10 * time.Second
	maxResponseBytes = 4 << 20
	dnsPageSize      = 100
	maxDNSPages      = 10_000
)

func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// requestJSON accepts only bounded JSON objects and successful HTTP statuses.
// Provider-specific success flags and required result fields are checked by callers.
func requestJSON(ctx context.Context, client *http.Client, method, requestURL, token string, payload, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read DNS API response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("DNS API response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("DNS API HTTP status %d", resp.StatusCode)
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return fmt.Errorf("invalid DNS API response: expected a JSON object")
	}
	if err := json.Unmarshal(data, result); err != nil {
		return fmt.Errorf("parse DNS API response: %w", err)
	}
	return nil
}
