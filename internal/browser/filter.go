package browser

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/mafredri/cdp"
	"github.com/mafredri/cdp/protocol/network"
)

// blockedURLPatterns returns the URL substrings passed to Network.setBlockedURLs.
// Chrome blocks a request of the page target whose URL contains one of these
// patterns. A cross-site iframe is its own target, so the list does not reach it.
// This operates within the normal request pipeline and does NOT bypass
// the HTTP disk cache for allowed requests.
// Font patterns are deliberately absent. The Cloudflare challenge page carries
// its fonts as data: URIs, so blocking them saves nothing and fails the challenge.
func blockedURLPatterns() []string {
	return []string{
		// Images — all formats. The challenge renders via canvas and inline
		// resources, not image tags. The hero images are the largest page downloads.
		".ico", ".gif", ".svg", ".webp", ".jpg", ".jpeg", ".png",
		// Stylesheets — not needed for Cloudflare challenge or cookie acquisition.
		".css",
		// Chrome internal URLs. Chrome starts on about:blank, so these are a backstop.
		"chrome://", "chrome-extension://",
		// Analytics & tracking
		"analytics", "tracking", "google-analytics",
		".googleapis.", "chargebee", "cookiebot", "beacon",
	}
}

// SetBlockedURLs configures Chrome to block unnecessary network requests
// (images, stylesheets, analytics) using the Network domain. Domain-level filtering
// is handled by the forward proxy. Must be called after Network.Enable.
func SetBlockedURLs(ctx context.Context, c *cdp.Client, logger *slog.Logger) error { // coverage-ignore: needs a live Chrome
	patterns := blockedURLPatterns()
	args := network.NewSetBlockedURLsArgs(patterns)
	if err := c.Network.SetBlockedURLs(ctx, args); err != nil {
		return fmt.Errorf("setting blocked URLs: %w", err)
	}
	logger.Debug("blocked URL patterns set", "count", len(patterns))
	return nil
}
