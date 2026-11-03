package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
)

// ErrInvalidURL is returned when a URL cannot be used as an item identity.
var ErrInvalidURL = errors.New("domain: invalid item url")

// trackingParams are dropped besides every utm_* parameter (same set as the
// upstream Horizon orchestrator).
var trackingParams = map[string]bool{
	"_ga": true, "dclid": true, "fbclid": true, "gclid": true, "igshid": true,
	"li_fat_id": true, "mc_cid": true, "mc_eid": true, "msclkid": true,
	"ttclid": true, "twclid": true, "vero_id": true,
}

// NormalizeURL returns the canonical form used for cross-source dedup:
// lowercase scheme/host, no "www.", no default port, no fragment, no utm_* or
// tracking params, no trailing slash. Remaining query params keep their order.
func NormalizeURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", ErrInvalidURL
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", ErrInvalidURL
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if host == "" {
		return "", ErrInvalidURL
	}
	if port := u.Port(); port != "" && !(scheme == "http" && port == "80") && !(scheme == "https" && port == "443") {
		host += ":" + port
	}

	var kept []string
	if u.RawQuery != "" {
		for _, part := range strings.Split(u.RawQuery, "&") {
			if part == "" {
				continue
			}
			name, _, _ := strings.Cut(part, "=")
			if dec, err := url.QueryUnescape(name); err == nil {
				name = dec
			}
			name = strings.ToLower(name)
			if strings.HasPrefix(name, "utm_") || trackingParams[name] {
				continue
			}
			kept = append(kept, part)
		}
	}

	var b strings.Builder
	b.WriteString(scheme)
	b.WriteString("://")
	b.WriteString(host)
	b.WriteString(strings.TrimRight(u.EscapedPath(), "/"))
	if len(kept) > 0 {
		b.WriteByte('?')
		b.WriteString(strings.Join(kept, "&"))
	}
	return b.String(), nil
}

// URLHash is the sha256 hex digest of an already normalized URL.
func URLHash(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}
