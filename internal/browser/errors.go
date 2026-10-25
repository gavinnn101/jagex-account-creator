package browser

import "errors"

var (
	// ErrChallengeTimeout indicates the Cloudflare challenge was not completed within
	// the configured timeout (e.g., expected cookies did not appear in time).
	ErrChallengeTimeout = errors.New("cloudflare challenge timed out")

	// ErrLoadTimeout indicates the page's load event did not fire within the
	// configured timeout. The DOM content was loaded but subresources (images,
	// scripts, etc.) may still be pending. This is typically non-fatal.
	ErrLoadTimeout = errors.New("page load timed out")

	// ErrProxyConnection indicates the proxy failed to establish a connection
	// (e.g., CONNECT tunnel failed). This is a proxy health issue, not a site issue.
	ErrProxyConnection = errors.New("proxy connection failed")

	// ErrUserAgentOverride indicates that a child target did not get the user
	// agent override, so its requests would carry Chrome's own UA.
	ErrUserAgentOverride = errors.New("a child target has no user agent override")
)
