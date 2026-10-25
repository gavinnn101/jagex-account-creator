package browser

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/mafredri/cdp"
	"github.com/mafredri/cdp/protocol/network"
)

// GetAllCookies retrieves all browser cookies via the Network domain.
// The Network domain must already be enabled before calling this function.
func GetAllCookies(ctx context.Context, c *cdp.Client) ([]network.Cookie, error) { // coverage-ignore: needs a live Chrome
	reply, err := c.Network.GetCookies(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("getting cookies: %w", err)
	}
	return reply.Cookies, nil
}

// ClearBrowserCookies deletes all browser cookies via the Network domain.
func ClearBrowserCookies(ctx context.Context, c *cdp.Client) error { // coverage-ignore: needs a live Chrome
	if err := c.Network.ClearBrowserCookies(ctx); err != nil {
		return fmt.Errorf("clearing browser cookies: %w", err)
	}
	return nil
}

// LogCookies logs the name and attributes of every browser cookie, but not
// its value.
func LogCookies(ctx context.Context, c *cdp.Client, logger *slog.Logger) { // coverage-ignore: needs a live Chrome
	// Log all cookies.
	allCookies, err := GetAllCookies(ctx, c)
	if err != nil {
		logger.Warn("failed to get cookies", "error", err)
	} else {
		logger.Debug("cookies extracted", "count", len(allCookies))
		for _, cookie := range allCookies {
			logger.Debug("cookie",
				"name", cookie.Name,
				"domain", cookie.Domain,
				"path", cookie.Path,
				"secure", cookie.Secure,
				"http_only", cookie.HTTPOnly,
				"value_len", len(cookie.Value),
			)
		}
	}
}
