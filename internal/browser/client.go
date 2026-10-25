// Package browser launches Chrome and drives it over the DevTools protocol.
package browser

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/mafredri/cdp"
	"github.com/mafredri/cdp/devtool"
	"github.com/mafredri/cdp/rpcc"
)

// Client wraps a CDP connection and provides access to protocol domains.
type Client struct {
	conn      *rpcc.Conn
	CDP       *cdp.Client
	closeOnce sync.Once
	closeErr  error
}

// NewClient connects to Chrome via CDP using the browser-level WebSocket URL.
// It discovers the first available page target and connects to it. It retries
// for up to 10 seconds to allow Chrome time to initialize.
func NewClient(ctx context.Context, wsURL string, logger *slog.Logger) (*Client, error) { // coverage-ignore: needs a live Chrome
	logger = logger.With("component", "cdp-client")

	devt := devtool.New(wsURLToHTTP(wsURL))

	// Chrome may not have a page target ready immediately. Retry.
	var pt *devtool.Target
	deadline := time.Now().Add(10 * time.Second)
	for {
		var err error
		pt, err = devt.Get(ctx, devtool.Page)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("getting page target (timed out): %w", err)
		}
		logger.Debug("waiting for page target", "error", err)
		t := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, fmt.Errorf("waiting for a page target: %w", ctx.Err())
		case <-t.C:
		}
	}
	logger.Debug("found page target", "url", pt.WebSocketDebuggerURL)

	conn, err := rpcc.DialContext(ctx, pt.WebSocketDebuggerURL)
	if err != nil {
		return nil, fmt.Errorf("dialing page target: %w", err)
	}

	c := cdp.NewClient(conn)
	logger.Debug("connected to CDP")

	return &Client{
		conn: conn,
		CDP:  c,
	}, nil
}

// Close closes the underlying CDP connection. It is safe to call multiple times;
// only the first call performs the close.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		if c.conn != nil {
			c.closeErr = c.conn.Close()
		}
	})
	return c.closeErr
}

// wsURLToHTTP converts a WebSocket URL like ws://127.0.0.1:9222/devtools/browser/XXX
// to an HTTP URL like http://127.0.0.1:9222 for the devtool HTTP API.
func wsURLToHTTP(wsURL string) string {
	rest, _ := strings.CutPrefix(wsURL, "ws://")
	host, _, _ := strings.Cut(rest, "/")
	return "http://" + host
}
