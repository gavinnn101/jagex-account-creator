package browser

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mafredri/cdp"
	"github.com/mafredri/cdp/protocol/page"

	"jagex_account_creator/internal/useragent"
)

// NavigateAndLoad navigates to the given URL, waits for DOMContentEventFired,
// then waits for LoadEventFired within the given timeout. Both event listeners
// are created BEFORE navigating to avoid race conditions where the load event
// fires between DOMContentEventFired and listener creation.
// The Page domain must already be enabled before calling this function.
func NavigateAndLoad(ctx context.Context, c *cdp.Client, url string, loadTimeout time.Duration) error {
	// Create BOTH listeners BEFORE navigating so we don't miss either event.
	domContent, err := c.Page.DOMContentEventFired(ctx)
	if err != nil {
		return fmt.Errorf("creating DOMContentEventFired listener: %w", err)
	}
	defer func() { _ = domContent.Close() }()

	loadEvent, err := c.Page.LoadEventFired(ctx)
	if err != nil {
		return fmt.Errorf("creating LoadEventFired listener: %w", err)
	}
	defer func() { _ = loadEvent.Close() }()

	navArgs := page.NewNavigateArgs(url)
	nav, err := c.Page.Navigate(ctx, navArgs)
	if err != nil {
		return fmt.Errorf("navigating to %s: %w", url, err)
	}
	if nav.ErrorText != nil {
		errText := *nav.ErrorText
		if isProxyNetError(errText) {
			return fmt.Errorf("navigation error for %s: %s: %w", url, errText, ErrProxyConnection)
		}
		return fmt.Errorf("navigation error for %s: %s", url, errText)
	}

	// Wait for DOM content to be parsed.
	if _, err := domContent.Recv(); err != nil {
		return fmt.Errorf("waiting for DOMContentEventFired on %s: %w", url, err)
	}

	// Wait for full page load with timeout. Recv() blocks on the stream's
	// creation context, so we use a goroutine + select to apply loadTimeout.
	loadCtx, loadCancel := context.WithTimeout(ctx, loadTimeout)
	defer loadCancel()

	recvCh := make(chan error, 1)
	go func() {
		_, err := loadEvent.Recv()
		recvCh <- err
	}()

	select {
	case <-loadCtx.Done():
		if ctx.Err() != nil {
			// Parent context cancelled — propagate as a real error.
			return fmt.Errorf("waiting for LoadEventFired on %s: %w", url, ctx.Err())
		}
		// Load timeout only — DOM content loaded but full load didn't complete.
		return ErrLoadTimeout
	case err := <-recvCh:
		if err != nil {
			return fmt.Errorf("waiting for LoadEventFired on %s: %w", url, err)
		}
		return nil
	}
}

// GetUserAgentAndHints reads navigator.userAgent and the client hints of the
// page, the high-entropy hints included, in one Runtime.Evaluate call.
func GetUserAgentAndHints(ctx context.Context, c *cdp.Client) (string, *useragent.ClientHints, error) { // coverage-ignore: needs a live Chrome
	var reading uaReading
	if err := evalAwaitingJS(ctx, c, userAgentJS, &reading); err != nil {
		return "", nil, err
	}
	hints, err := reading.clientHints()
	if err != nil {
		return "", nil, err
	}
	return reading.UserAgent, hints, nil
}

// WaitForJS polls until the given JavaScript expression returns true, or the
// timeout expires. Any other value reads as not met. This is useful for
// waiting on elements inside shadow DOMs or other conditions that a simple CSS
// selector cannot express.
func WaitForJS(ctx context.Context, c *cdp.Client, js string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// An evaluation error reads as "not met", so the wait goes on until the timeout.
	met := func() bool {
		var found bool
		return EvalJS(ctx, c, js, &found) == nil && found
	}

	// Check immediately before entering the ticker loop.
	if met() {
		return nil
	}

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for the JS condition: %w", ctx.Err())
		case <-ticker.C:
			if met() {
				return nil
			}
		}
	}
}

// LogPageState logs the current page URL and title.
func LogPageState(ctx context.Context, c *cdp.Client, logger *slog.Logger, label string) {
	js := `JSON.stringify({url: location.href, title: document.title})`
	var raw string
	if err := EvalJS(ctx, c, js, &raw); err != nil {
		logger.Warn("could not get page state", "label", label, "error", err)
		return
	}
	logger.Debug("page state", "label", label, "data", raw)
}

// isProxyNetError returns true if the Chrome net error indicates a proxy
// connectivity failure (as opposed to a target site issue).
func isProxyNetError(errText string) bool {
	return strings.Contains(errText, "ERR_TUNNEL_CONNECTION_FAILED") ||
		strings.Contains(errText, "ERR_PROXY_CONNECTION_FAILED")
}
