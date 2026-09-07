package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strings"
	"time"
)

const DefaultUploadTimeout = 60 * time.Second

// Config holds DNS upload configuration.
type Config struct {
	Provider    string // "cloudflare" or "vercel"
	Token       string // API token
	Zone        string // Zone ID (Cloudflare) or domain (Vercel)
	Subdomain   string // Subdomain prefix (e.g., "cf" for cf.example.com)
	UploadCount int    // Number of IPs to upload
	TeamID      string // Vercel Team ID (optional)
}

// Record is an A/AAAA record scoped to the subdomain passed to the provider.
// The writable properties are retained so a deleted record can be restored.
type Record struct {
	ID             string          `json:"id,omitempty"`
	Type           string          `json:"type"`
	Content        string          `json:"content"`
	TTL            int             `json:"ttl"`
	Proxied        bool            `json:"proxied,omitempty"`
	Comment        string          `json:"comment,omitempty"`
	Tags           []string        `json:"tags,omitempty"`
	Settings       json.RawMessage `json:"settings,omitempty"`
	PrivateRouting bool            `json:"private_routing,omitempty"`
}

// Provider only returns A/AAAA records for the exact requested subdomain.
// ListRecords must complete pagination before returning a usable snapshot.
type Provider interface {
	Name() string
	ListRecords(ctx context.Context, subdomain string) ([]Record, error)
	CreateRecord(ctx context.Context, subdomain string, record Record) (Record, error)
	DeleteRecord(ctx context.Context, recordID string) error
}

// NewProvider creates a Provider based on the config.
func NewProvider(cfg Config) (Provider, error) {
	if _, err := normalizeSubdomain(cfg.Subdomain); err != nil {
		return nil, err
	}
	switch cfg.Provider {
	case "cloudflare":
		token := cfg.Token
		if token == "" {
			token = os.Getenv("CF_API_TOKEN")
		}
		zone := cfg.Zone
		if zone == "" {
			zone = os.Getenv("CF_ZONE_ID")
		}
		if token == "" {
			return nil, fmt.Errorf("cloudflare: API token required (--dns-token or CF_API_TOKEN)")
		}
		if zone == "" {
			return nil, fmt.Errorf("cloudflare: zone ID required (--dns-zone or CF_ZONE_ID)")
		}
		return NewCloudflareProvider(token, zone), nil

	case "vercel":
		token := cfg.Token
		if token == "" {
			token = os.Getenv("VERCEL_TOKEN")
		}
		teamID := cfg.TeamID
		if teamID == "" {
			teamID = os.Getenv("VERCEL_TEAM_ID")
		}
		domain := cfg.Zone
		if token == "" {
			return nil, fmt.Errorf("vercel: API token required (--dns-token or VERCEL_TOKEN)")
		}
		if domain == "" {
			return nil, fmt.Errorf("vercel: domain required (--dns-zone)")
		}
		if _, err := normalizeDomain(domain); err != nil {
			return nil, fmt.Errorf("vercel: invalid domain: %w", err)
		}
		return NewVercelProvider(token, domain, teamID), nil

	default:
		return nil, fmt.Errorf("unknown DNS provider: %s (supported: cloudflare, vercel)", cfg.Provider)
	}
}

// Upload reconciles the supplied address families, leaving other families alone.
// Old records are only removed after all new records are confirmed and visible.
// A failed deletion triggers bounded, best-effort restoration of old properties;
// new records are retained, so recovery does not remove the replacement addresses.
func Upload(ctx context.Context, provider Provider, subdomain string, ips []netip.Addr, verbose bool) error {
	if len(ips) == 0 {
		return nil
	}
	name, err := normalizeSubdomain(subdomain)
	if err != nil {
		return err
	}
	desired := make(map[string]bool)
	families := make(map[string]bool)
	var wanted []Record
	for _, ip := range ips {
		if !ip.IsValid() || ip.Zone() != "" {
			return fmt.Errorf("invalid DNS upload address %q", ip)
		}
		ip = ip.Unmap()
		r := Record{Type: "A", Content: ip.String()}
		if ip.Is6() {
			r.Type = "AAAA"
		}
		key, _ := recordKey(r)
		if !desired[key] {
			wanted = append(wanted, r)
			desired[key] = true
		}
		families[r.Type] = true
	}

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultUploadTimeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Recovery shares the caller's hard deadline and cancellation signal. Reserve
	// a quarter of the remaining time, capped at 15s, for restoration if needed.
	deadline, _ := ctx.Deadline()
	reserve := min(time.Until(deadline)/4, 15*time.Second)
	opCtx, cancel := context.WithDeadline(ctx, deadline.Add(-reserve))
	defer cancel()

	snapshot, err := provider.ListRecords(opCtx, name)
	if err != nil {
		return fmt.Errorf("read DNS snapshot: %w", err)
	}
	_, existing, err := indexRecords(snapshot)
	if err != nil {
		return err
	}
	var additions, removals []Record
	for _, r := range wanted {
		key, _ := recordKey(r)
		if !existing[key] {
			additions = append(additions, r)
		}
	}
	for _, r := range snapshot {
		key, _ := recordKey(r)
		if families[r.Type] && !desired[key] {
			removals = append(removals, r)
		}
	}
	sort.Slice(removals, func(i, j int) bool { return removals[i].ID < removals[j].ID })
	if len(additions) == 0 && len(removals) == 0 {
		return nil
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "dns: updating %s: add=%d remove=%d\n", subdomain, len(additions), len(removals))
	}
	var created []Record
	for _, r := range additions {
		added, err := createChecked(opCtx, provider, name, r)
		if err != nil {
			return fmt.Errorf("create %s %s (old records retained; %d additions confirmed): %w", r.Type, r.Content, len(created), err)
		}
		created = append(created, added)
	}

	current, err := provider.ListRecords(opCtx, name)
	if err != nil {
		return fmt.Errorf("confirm DNS additions (old records retained): %w", err)
	}
	byID, present, err := indexRecords(current)
	if err != nil {
		return err
	}
	for key := range desired {
		if !present[key] {
			return fmt.Errorf("DNS candidate %s is not visible; old records retained", key)
		}
	}
	for _, r := range created {
		got, ok := byID[r.ID]
		wantKey, _ := recordKey(r)
		gotKey, _ := recordKey(got)
		if !ok || gotKey != wantKey {
			return fmt.Errorf("new DNS record %s is not visible; old records retained", r.ID)
		}
	}
	for _, r := range snapshot {
		if families[r.Type] && !sameRecord(byID[r.ID], r) {
			return fmt.Errorf("DNS record %s changed during upload; old records retained", r.ID)
		}
	}
	plannedRemoval := make(map[string]bool, len(removals))
	for _, r := range removals {
		plannedRemoval[r.ID] = true
	}
	for _, r := range current {
		key, _ := recordKey(r)
		if families[r.Type] && !desired[key] && !plannedRemoval[r.ID] {
			return fmt.Errorf("unexpected DNS record %s appeared during upload; old records retained", r.ID)
		}
	}

	var attempted []Record
	for _, r := range removals {
		if err := opCtx.Err(); err != nil {
			return recoverUpload(ctx, provider, name, attempted, err)
		}
		// Include the current attempt: a failed response can follow a successful
		// server-side deletion, so recovery must check it too.
		attempted = append(attempted, r)
		if err := provider.DeleteRecord(opCtx, r.ID); err != nil {
			return recoverUpload(ctx, provider, name, attempted, fmt.Errorf("delete DNS record %s: %w", r.ID, err))
		}
	}
	if len(attempted) > 0 {
		current, err = provider.ListRecords(opCtx, name)
		if err == nil {
			err = verifyDesired(current, desired, families)
		}
		if err != nil {
			return recoverUpload(ctx, provider, name, attempted, fmt.Errorf("verify DNS update: %w", err))
		}
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "dns: upload complete (%d addresses)\n", len(wanted))
	}
	return nil
}

func createChecked(ctx context.Context, provider Provider, name string, r Record) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	created, err := provider.CreateRecord(ctx, name, r)
	if err != nil {
		return Record{}, err
	}
	want, _ := recordKey(r)
	got, err := recordKey(created)
	if err != nil || created.ID == "" || got != want {
		return Record{}, fmt.Errorf("invalid DNS creation response")
	}
	return created, nil
}

func indexRecords(records []Record) (map[string]Record, map[string]bool, error) {
	byID := make(map[string]Record)
	keys := make(map[string]bool)
	for _, r := range records {
		if r.Type != "A" && r.Type != "AAAA" {
			continue
		}
		key, err := recordKey(r)
		if err != nil || r.ID == "" || r.TTL < 1 || (len(r.Settings) > 0 && !json.Valid(r.Settings)) {
			return nil, nil, fmt.Errorf("invalid DNS record in snapshot: id=%q type=%q", r.ID, r.Type)
		}
		if _, duplicate := byID[r.ID]; duplicate {
			return nil, nil, fmt.Errorf("duplicate DNS record ID %q", r.ID)
		}
		byID[r.ID] = r
		keys[key] = true
	}
	return byID, keys, nil
}

func checkedRecords(records []Record) ([]Record, error) {
	_, _, err := indexRecords(records)
	return records, err
}

func recordKey(r Record) (string, error) {
	ip, err := netip.ParseAddr(r.Content)
	if err != nil || ip.Zone() != "" || (r.Type == "A" && !ip.Is4()) || (r.Type == "AAAA" && !ip.Is6()) || (r.Type != "A" && r.Type != "AAAA") {
		return "", fmt.Errorf("invalid %s address %q", r.Type, r.Content)
	}
	return r.Type + "/" + ip.String(), nil
}

func sameRecord(a, b Record) bool {
	// Record IDs change on restoration. Compare all writable properties instead.
	a.ID, b.ID = "", ""
	for _, r := range []*Record{&a, &b} {
		if ip, err := netip.ParseAddr(r.Content); err == nil && (r.Type == "A" || r.Type == "AAAA") {
			r.Content = ip.String()
		}
		if len(r.Settings) > 0 {
			// RawMessage preserves input key order; decode and re-encode before
			// comparison so equivalent API representations do not look changed.
			var settings any
			if !json.Valid(r.Settings) {
				return false
			}
			decoder := json.NewDecoder(bytes.NewReader(r.Settings))
			decoder.UseNumber()
			if err := decoder.Decode(&settings); err != nil {
				return false
			}
			if obj, ok := settings.(map[string]any); ok && len(obj) == 0 {
				settings = nil
			}
			r.Settings = nil
			if settings != nil {
				r.Settings, _ = json.Marshal(settings)
			}
		}
	}
	a.Tags, b.Tags = append([]string(nil), a.Tags...), append([]string(nil), b.Tags...)
	sort.Strings(a.Tags)
	sort.Strings(b.Tags)
	ax, errA := json.Marshal(a)
	bx, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ax, bx)
}

func verifyDesired(records []Record, desired, families map[string]bool) error {
	_, present, err := indexRecords(records)
	if err != nil {
		return err
	}
	for key := range desired {
		if !present[key] {
			return fmt.Errorf("DNS candidate %s is missing", key)
		}
	}
	for _, r := range records {
		key, _ := recordKey(r)
		if families[r.Type] && !desired[key] {
			return fmt.Errorf("unexpected DNS record %s remains", r.ID)
		}
	}
	return nil
}

func recoverUpload(ctx context.Context, provider Provider, name string, attempted []Record, cause error) error {
	if len(attempted) == 0 {
		return fmt.Errorf("DNS update stopped; old records retained: %w", cause)
	}
	if err := restoreRecords(ctx, provider, name, attempted); err != nil {
		return errors.Join(cause, fmt.Errorf("DNS recovery incomplete; new records retained: %w", err))
	}
	return fmt.Errorf("DNS update failed; original records restored and new records retained: %w", cause)
}

func restoreRecords(ctx context.Context, provider Provider, name string, attempted []Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := provider.ListRecords(ctx, name)
	if err != nil {
		return err
	}
	if _, _, err := indexRecords(current); err != nil {
		return err
	}
	var recoveryErrors []error
	for _, old := range attempted {
		found, conflict := false, false
		oldKey, _ := recordKey(old)
		for _, r := range current {
			key, _ := recordKey(r)
			if key == oldKey || r.ID == old.ID {
				if sameRecord(r, old) {
					found = true
				} else {
					conflict = true
				}
			}
		}
		if found {
			continue
		}
		if conflict {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("old record %s conflicts with a current record", old.ID))
			continue
		}
		restored, err := createChecked(ctx, provider, name, old)
		if err != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("restore %s %s: %w", old.Type, old.Content, err))
			if ctx.Err() != nil {
				break
			}
			continue
		}
		current = append(current, restored)
	}
	// Re-read after restoration rather than treating a successful POST as proof.
	if ctx.Err() == nil {
		current, err = provider.ListRecords(ctx, name)
		if err == nil {
			_, _, err = indexRecords(current)
		}
		if err != nil {
			recoveryErrors = append(recoveryErrors, err)
		} else {
			for _, old := range attempted {
				found := false
				for _, r := range current {
					if sameRecord(r, old) {
						found = true
						break
					}
				}
				if !found {
					recoveryErrors = append(recoveryErrors, fmt.Errorf("old record %s is not restored", old.ID))
				}
			}
		}
	} else {
		recoveryErrors = append(recoveryErrors, ctx.Err())
	}
	return errors.Join(recoveryErrors...)
}

func normalizeSubdomain(name string) (string, error) {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if name == "" || name == "@" {
		return "", nil
	}
	if err := validateDNSName(name, true); err != nil {
		return "", fmt.Errorf("invalid DNS subdomain %q: %w", name, err)
	}
	return name, nil
}

func normalizeDomain(name string) (string, error) {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if err := validateDNSName(name, false); err != nil {
		return "", err
	}
	return name, nil
}

func validateDNSName(name string, wildcard bool) error {
	if name == "" || len(name) > 253 {
		return errors.New("name must contain 1..253 characters")
	}
	for i, label := range strings.Split(name, ".") {
		if wildcard && i == 0 && label == "*" {
			continue
		}
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("invalid DNS label")
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z') && !(ch >= '0' && ch <= '9') && ch != '-' && ch != '_' {
				return errors.New("use DNS labels, not a URL or query string")
			}
		}
	}
	return nil
}
