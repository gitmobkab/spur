package useragent

import "testing"

func TestClassify(t *testing.T) {
	for ua, want := range map[string]string{
		"":           "unknown",
		"curl/8.5.0": "bot",
		"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)":                              "bot",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148":             "mobile",
		"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Chrome/120.0 Mobile Safari/537.36":         "mobile",
		"Mozilla/5.0 (Linux; Android 13; SM-X700) AppleWebKit/537.36 Chrome/120.0 Safari/537.36":                "tablet",
		"Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15":                                    "tablet",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36": "desktop",
	} {
		if got := Classify(ua); got != want {
			t.Errorf("Classify(%q) = %q, want %q", ua, got, want)
		}
	}
}
