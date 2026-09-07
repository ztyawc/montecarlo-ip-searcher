package probe

import (
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// ValidateDownloadURL checks a custom source without resolving or dialing it.
// User information and fragments are rejected because they cannot be faithfully
// represented by a candidate-IP download measurement.
func ValidateDownloadURL(raw string) error {
	_, err := parseDownloadURL(raw)
	return err
}

func parseDownloadURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		// url.Error includes the entire input, which can contain credentials.
		if parseErr, ok := err.(*url.Error); ok {
			err = parseErr.Err
		}
		return nil, fmt.Errorf("invalid download URL: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") || u.Opaque != "" {
		return nil, fmt.Errorf("download URL must use https://")
	}
	if u.User != nil {
		return nil, fmt.Errorf("download URL must not contain user information")
	}
	if strings.Contains(raw, "#") {
		return nil, fmt.Errorf("download URL must not contain a fragment")
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("download URL must contain a hostname")
	}
	if strings.HasSuffix(u.Host, ":") {
		return nil, fmt.Errorf("download URL has an empty port")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return nil, fmt.Errorf("download URL port must be in 1..65535")
		}
	}
	if strings.HasPrefix(u.Host, "[") || strings.Contains(host, ":") {
		ip, err := netip.ParseAddr(host)
		if err != nil || !ip.Is6() || ip.Zone() != "" || !strings.HasPrefix(u.Host, "[") {
			return nil, fmt.Errorf("download URL must use a bracketed IPv6 address without a zone")
		}
	} else {
		name := strings.TrimSuffix(host, ".")
		if len(name) == 0 || len(name) > 253 {
			return nil, fmt.Errorf("download URL has an invalid hostname")
		}
		for _, label := range strings.Split(name, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return nil, fmt.Errorf("download URL has an invalid hostname label")
			}
			for _, ch := range label {
				if !(ch >= 'a' && ch <= 'z') && !(ch >= 'A' && ch <= 'Z') && !(ch >= '0' && ch <= '9') && ch != '-' && ch != '_' {
					return nil, fmt.Errorf("download URL hostname must use ASCII or Punycode DNS labels")
				}
			}
		}
	}
	u.Scheme = "https"
	if u.Path == "" {
		u.Path = "/"
	}
	return u, nil
}
