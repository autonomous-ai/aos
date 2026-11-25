// Package otamirror maps OTA download URLs between the update bucket's
// storage host and the CDN that fronts the same bucket.
//
// The CDN serves the bucket byte for byte (same object, same ETag), and it is
// reachable from networks that block the storage host. Devices therefore try
// the CDN copy first and keep the storage URL as a fallback, so a device that
// can reach either host still updates.
package otamirror

import (
	"net/url"
	"strings"
)

// Mapping pairs the storage location of the OTA bucket with the CDN in front of it.
type Mapping struct {
	// CDN is the CDN origin, e.g. "https://cdn.autonomous.ai" (no trailing slash).
	CDN string
	// Storage is the bucket's public storage URL prefix, e.g.
	// "https://storage.googleapis.com/<bucket>" (no trailing slash).
	Storage string
}

// Default is the production OTA bucket and its CDN.
var Default = Mapping{
	CDN:     "https://cdn.autonomous.ai",
	Storage: "https://storage.googleapis.com/s3-autonomous-upgrade-3",
}

// ToCDN returns the CDN URL for a URL inside the storage bucket, or raw
// unchanged when it points anywhere else (another bucket, another host, or an
// unparseable value). Query and fragment are kept.
func (m Mapping) ToCDN(raw string) string {
	rest, ok := cut(raw, m.Storage)
	if !ok {
		return raw
	}
	return m.CDN + rest
}

// ToStorage is the inverse of ToCDN: a CDN URL becomes the bucket URL.
func (m Mapping) ToStorage(raw string) string {
	rest, ok := cut(raw, m.CDN)
	if !ok {
		return raw
	}
	return m.Storage + rest
}

// Candidates lists the URLs to try for raw, in order: the CDN copy first, then
// the storage URL. URLs outside the bucket yield just themselves.
func (m Mapping) Candidates(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	cdn := m.ToCDN(raw)
	storage := m.ToStorage(cdn)
	if cdn == storage {
		return []string{raw}
	}
	return []string{cdn, storage}
}

// cut matches raw against prefix by scheme, host (case-insensitive) and path
// segment boundary, and returns the remainder starting at "/" (or "" when raw
// is exactly the prefix), with any query and fragment.
func cut(raw, prefix string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || prefix == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return "", false
	}
	p, err := url.Parse(prefix)
	if err != nil || p.Host == "" {
		return "", false
	}
	if !strings.EqualFold(u.Scheme, p.Scheme) && !(strings.EqualFold(u.Scheme, "http") && strings.EqualFold(p.Scheme, "https")) {
		return "", false
	}
	if !strings.EqualFold(u.Host, p.Host) {
		return "", false
	}
	base := strings.TrimSuffix(p.Path, "/")
	path := u.EscapedPath()
	if base != "" {
		if path != base && !strings.HasPrefix(path, base+"/") {
			return "", false
		}
		path = strings.TrimPrefix(path, base)
	}
	if path != "" && !strings.HasPrefix(path, "/") {
		return "", false
	}
	rest := path
	if u.RawQuery != "" {
		rest += "?" + u.RawQuery
	}
	if u.Fragment != "" {
		rest += "#" + u.EscapedFragment()
	}
	return rest, true
}

// ToCDN maps raw with the Default mapping.
func ToCDN(raw string) string { return Default.ToCDN(raw) }

// Candidates lists raw's download URLs with the Default mapping.
func Candidates(raw string) []string { return Default.Candidates(raw) }
