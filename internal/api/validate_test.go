package api

import "testing"

func TestValidateTarget(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		{"https://example.com/path?q=1", true},
		{"http://example.com", true},
		{"https://93.184.216.34/", true},
		{"ftp://example.com", false},
		{"javascript:alert(1)", false},
		{"https://user:pass@example.com", false},
		{"https://google.com@evil.com", false},
		{"http://localhost:8080", false},
		{"http://127.0.0.1", false},
		{"http://10.0.0.5", false},
		{"http://192.168.1.1", false},
		{"http://169.254.169.254/latest/meta-data", false},
		{"http://[::1]/", false},
		{"http://postgres.railway.internal", false},
		{"https://spur.example.com/abc", false}, // self
		{"https://", false},
	}
	for _, c := range cases {
		_, err := validateTarget(c.url, "spur.example.com")
		if (err == nil) != c.ok {
			t.Errorf("validateTarget(%q) err=%v, want ok=%v", c.url, err, c.ok)
		}
	}
}

func TestValidSlug(t *testing.T) {
	for s, want := range map[string]bool{
		"abc": true, "my-link_2": true, "ab": false, "api": false, "API": false,
		"has space": false, "../etc": false, "healthz": false,
	} {
		if got := validSlug(s); got != want {
			t.Errorf("validSlug(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestNewSlug(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		s := newSlug()
		if len(s) != 7 || !slugPattern.MatchString(s) {
			t.Fatalf("bad slug %q", s)
		}
		seen[s] = true
	}
	if len(seen) < 999 {
		t.Errorf("too many collisions: %d unique of 1000", len(seen))
	}
}
