package api

import (
	"crypto/rand"
	"errors"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
)

const maxURLLen = 2048

var (
	slugPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{3,32}$`)
	reservedSlugs = map[string]bool{"api": true, "healthz": true, "favicon.ico": true, "robots.txt": true}
)

func validSlug(s string) bool {
	return slugPattern.MatchString(s) && !reservedSlugs[strings.ToLower(s)]
}

const slugAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// newSlug returns a random 7-char slug from an alphabet without look-alike characters.
func newSlug() string {
	return randomFrom(slugAlphabet, 7)
}

func randomFrom(alphabet string, n int) string {
	b := make([]byte, n)
	// Rejection sampling keeps the distribution uniform.
	limit := 256 - 256%len(alphabet)
	buf := make([]byte, 1)
	for i := 0; i < n; {
		rand.Read(buf)
		if int(buf[0]) < limit {
			b[i] = alphabet[int(buf[0])%len(alphabet)]
			i++
		}
	}
	return string(b)
}

// validateTarget rejects destinations that are malformed, non-web, deceptive or internal.
// selfHost is spur's own host, blocked to prevent redirect loops.
func validateTarget(raw, selfHost string) (string, error) {
	if len(raw) > maxURLLen {
		return "", errors.New("url too long")
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", errors.New("url is not valid")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("url must be http or https")
	}
	if u.User != nil {
		return "", errors.New("url must not contain credentials")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", errors.New("url must have a host")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".local") {
		return "", errors.New("url points to an internal host")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
			return "", errors.New("url points to a private address")
		}
	}
	if selfHost != "" && host == strings.ToLower(selfHost) {
		return "", errors.New("url must not point back to spur")
	}
	return u.String(), nil
}
