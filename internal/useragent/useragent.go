// Package useragent does coarse, dependency-free device classification.
package useragent

import "strings"

var botMarkers = []string{
	"bot", "crawler", "spider", "slurp", "curl", "wget", "httpie", "python-requests",
	"go-http-client", "headless", "preview", "facebookexternalhit", "okhttp",
}

// Classify returns one of "bot", "tablet", "mobile", "desktop" or "unknown".
func Classify(ua string) string {
	l := strings.ToLower(ua)
	switch {
	case l == "":
		return "unknown"
	case containsAny(l, botMarkers):
		return "bot"
	case strings.Contains(l, "ipad"), strings.Contains(l, "tablet"),
		strings.Contains(l, "android") && !strings.Contains(l, "mobi"):
		return "tablet"
	case strings.Contains(l, "mobi"), strings.Contains(l, "iphone"):
		return "mobile"
	default:
		return "desktop"
	}
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
