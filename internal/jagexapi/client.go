// Package jagexapi sends the Jagex requests through a TLS-fingerprinted client.
package jagexapi

import (
	"context"
	"log/slog"
	"maps"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sardanioss/httpcloak/client"

	"jagex_account_creator/internal/useragent"
)

// presetName follows ShapeChromeMajor, so the TLS fingerprint and the request shapes show one Chrome.
func presetName() string { return "chrome-" + strconv.Itoa(ShapeChromeMajor) }

// Client wraps an httpcloak client instance with the browser session's header values, cookie import and proxy support.
type Client struct {
	inner  *client.Client
	doFn   func(ctx context.Context, req *client.Request) (*client.Response, error) // test override
	logger *slog.Logger
	values map[string]string // the header values of the browser session: the user agent and the hints
}

// NewClient creates a new TLS-fingerprinted HTTP client. It routes through
// proxyURL, or connects directly when proxyURL is empty. userAgent and hints
// must be the browser's, because Cloudflare compares them.
// An unknown presetName needs no check here: httpcloak stores it as a config
// error and the first Do returns it.
func NewClient(logger *slog.Logger, userAgent string, hints useragent.ClientHints, proxyURL string) *Client {
	logger = logger.With("component", "http-client")
	opts := clientOptions()
	if proxyURL != "" {
		opts = append(opts, client.WithProxy(proxyURL))
		logger.Debug("proxy set", "proxy", proxyURL)
	}
	var hintArgs []any
	for name, value := range hints.Headers() {
		// The log keys are the header names in snake case.
		hintArgs = append(hintArgs, strings.ReplaceAll(name, "-", "_"), value)
	}
	logger.Debug("client hints set", hintArgs...)
	inner := client.NewClient(presetName(), opts...)
	inner.EnableCookies()

	return &Client{
		inner:  inner,
		logger: logger,
		values: headerValues(userAgent, hints),
	}
}

// headerValues returns the header values of the browser session: the user agent and the hints.
func headerValues(userAgent string, hints useragent.ClientHints) map[string]string {
	values := maps.Collect(hints.Headers())
	values["user-agent"] = userAgent
	return values
}

// clientOptions returns the httpcloak options of the client, without the proxy.
// In TLS-only mode the preset sets the TLS and HTTP/2 fingerprint and adds no
// header. Each request names every header it sends (see requestShape).
func clientOptions() []client.Option {
	return []client.Option{
		client.WithTimeout(30 * time.Second),
		client.WithoutRedirects(),
		client.WithDisableHTTP3(),
		client.WithTLSOnly(),
	}
}

// Close releases resources held by the client.
func (c *Client) Close() {
	c.inner.Close()
}

// Do executes an HTTP request. A test sets doFn to replace the httpcloak call.
func (c *Client) Do(ctx context.Context, req *client.Request) (*client.Response, error) {
	doFunc := c.inner.Do
	if c.doFn != nil {
		doFunc = c.doFn
	}
	return doFunc(ctx, req)
}

// ImportCookies adds cookies to the client's jar. The jar keys each cookie by
// its own Domain and replaces a stored cookie with the same name and path.
// A cookie with an empty Domain is skipped.
func (c *Client) ImportCookies(cookies []client.Cookie) {
	imported := make([]*client.Cookie, 0, len(cookies))
	for i := range cookies {
		// The jar would store a cookie with no Domain under the key "", where no request finds it.
		if cookies[i].Domain == "" {
			continue
		}
		c.logger.Debug("importing cookie",
			"name", cookies[i].Name,
			"domain", cookies[i].Domain,
			"path", cookies[i].Path,
			"secure", cookies[i].Secure,
			"http_only", cookies[i].HttpOnly,
			"value_len", len(cookies[i].Value),
		)
		cp := cookies[i]
		imported = append(imported, &cp)
	}
	// SetCookies reads no URL: the jar keys each cookie by its own Domain.
	c.inner.Cookies().SetCookies(nil, imported)
	c.logger.Debug("imported cookies total", "count", len(imported))
}

// DebugLogCookies logs all cookies in the jar, grouped by domain.
func (c *Client) DebugLogCookies(targetURL string) {
	jar := c.inner.Cookies()
	all := jar.AllCookies()
	c.logger.Debug("cookie jar contents", "domains", len(all), "url", targetURL)
	for domain, cookies := range all {
		for _, cookie := range cookies {
			c.logger.Debug("jar cookie",
				"domain", domain,
				"name", cookie.Name,
				"cookie_domain", cookie.Domain,
				"path", cookie.Path,
				"secure", cookie.Secure,
				"value_len", len(cookie.Value),
			)
		}
	}
	// Also check what cookies would be sent for the target URL.
	if targetURL != "" {
		u, err := url.Parse(targetURL)
		if err == nil {
			sent := jar.Cookies(u)
			names := make([]string, len(sent))
			for i, ck := range sent {
				names[i] = ck.Name
			}
			c.logger.Debug("cookies that would be sent", "url", targetURL, "count", len(sent), "names", names)
		}
	}
}
