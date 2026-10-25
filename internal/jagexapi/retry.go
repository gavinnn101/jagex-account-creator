package jagexapi

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/sardanioss/httpcloak/client"
)

// retryConfig holds parameters for retry behavior.
type retryConfig struct {
	maxRetries int
	baseDelay  time.Duration
}

// defaultRetryConfig returns the production retry configuration.
func defaultRetryConfig() retryConfig {
	return retryConfig{
		maxRetries: 2,
		baseDelay:  500 * time.Millisecond,
	}
}

// doFunc is the signature for an HTTP request executor (matches client.Client.Do).
type doFunc func(ctx context.Context, req *client.Request) (*client.Response, error)

// doWithRetry sends req. It sends a GET again after a transport error or a 5xx,
// up to cfg.maxRetries times. It sends any other method once, because each POST
// moves a server state machine and the page sends it once. A single send returns
// its bare error. No send repeats after a 4xx, a 429 or the end of ctx.
// A 429 returns as an error that wraps ErrRateLimited, with no response.
// The 429 error and the last 5xx error carry the summary of describeResponse.
func doWithRetry(ctx context.Context, logger *slog.Logger, req *client.Request, do doFunc, cfg retryConfig) (*client.Response, error) {
	sends := 1
	if req.Method == "GET" {
		sends += cfg.maxRetries
	}
	var lastErr error
	for attempt := range sends {
		if attempt > 0 {
			if err := waitToRetry(ctx, logger, req, cfg, attempt, lastErr); err != nil {
				return nil, err
			}
		}

		resp, err := do(ctx, req)
		if err != nil {
			// Don't retry if the parent context is done.
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			continue
		}

		if resp.StatusCode == 429 {
			return nil, rateLimitedError(ctx, logger, resp)
		}

		// Retry on 5xx server errors.
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("server error: %s", describeResponse(ctx, logger, resp))
			_ = resp.Close()
			continue
		}

		return resp, nil
	}

	if sends == 1 {
		return nil, lastErr
	}
	return nil, fmt.Errorf("after %d attempts: %w", sends, lastErr)
}

// waitToRetry waits the backoff delay of retry number attempt. It fails when ctx ends.
func waitToRetry(ctx context.Context, logger *slog.Logger, req *client.Request, cfg retryConfig, attempt int, lastErr error) error {
	delay := cfg.baseDelay * (1 << (attempt - 1))
	logger.Warn("retrying request",
		"path", requestPath(req.URL), "method", req.Method,
		"attempt", attempt+1, "delay", delay, "error", lastErr)

	t := time.NewTimer(delay)
	select {
	case <-ctx.Done():
		t.Stop()
		return fmt.Errorf("waiting to retry: %w", ctx.Err())
	case <-t.C:
		return nil
	}
}

// rateLimitedError closes a 429 response and returns its ErrRateLimited error.
// The text leaves out the URL, because an oauth2/auth URL holds a one-time verifier.
func rateLimitedError(ctx context.Context, logger *slog.Logger, resp *client.Response) error {
	err := fmt.Errorf("%w: %s", ErrRateLimited, describeResponse(ctx, logger, resp))
	_ = resp.Close()
	return err
}
