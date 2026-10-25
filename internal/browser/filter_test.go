package browser

import (
	"strings"
	"testing"
)

func TestBlockedURLPatternsContainsExpectedEntries(t *testing.T) {
	expected := []string{
		// Images
		".jpg", ".jpeg", ".png",
		// Stylesheets
		".css",
		// Chrome internal
		"chrome://", "chrome-extension://",
		// Analytics (from original list)
		"analytics", "google-analytics",
	}

	blocked := blockedURLPatterns()
	patterns := make(map[string]bool, len(blocked))
	for _, p := range blocked {
		patterns[p] = true
	}

	for _, e := range expected {
		if !patterns[e] {
			t.Errorf("blockedURLPatterns missing expected pattern %q", e)
		}
	}
}

// Fonts must stay unblocked. The Cloudflare challenge page carries its fonts as
// data: URIs, so these patterns save no bandwidth and fail the challenge.
func TestBlockedURLPatternsDoNotBlockFonts(t *testing.T) {
	forbidden := []string{".woff", ".woff2", ".woff2!static", ".ttf", ".otf", ".eot", "x-font-woff"}

	for _, f := range forbidden {
		for _, p := range blockedURLPatterns() {
			if p == f {
				t.Errorf("blockedURLPatterns must not contain the font pattern %q", f)
			}
		}
	}
}

// No pattern may contain "data:". Chrome matches blocked-URL patterns against
// data: URIs, and the challenge page inlines its own assets that way.
func TestBlockedURLPatternsNeverMatchDataURIs(t *testing.T) {
	for _, p := range blockedURLPatterns() {
		if strings.Contains(p, "data:") {
			t.Errorf("blockedURLPatterns must not contain the data: pattern %q", p)
		}
	}
}

func TestBlockedURLPatternsDoNotBlockCriticalResources(t *testing.T) {
	// These URLs must NOT be blocked — they're essential for the Cloudflare
	// challenge and registration flow.
	criticalURLs := []string{
		"https://account.jagex.com/en-GB/login/registration-start",
		"https://account.jagex.com/cdn-cgi/challenge-platform/h/g/orchestrate/chl_page/v1",
		"https://challenges.cloudflare.com/turnstile/v0/g/api.js",
		"https://account.jagex.com/api/auth/consent",
		// The challenge page's own inline font.
		"data:application/x-font-woff;charset=utf-8;base64,d09GRgABAAAAAbDsABEAAAADsEQ",
	}

	for _, url := range criticalURLs {
		for _, pattern := range blockedURLPatterns() {
			if strings.Contains(url, pattern) {
				t.Errorf("critical URL %q would be blocked by pattern %q", url, pattern)
			}
		}
	}
}
