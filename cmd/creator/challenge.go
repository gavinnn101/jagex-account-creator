package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/mafredri/cdp"

	"jagex_account_creator/internal/browser"
	"jagex_account_creator/internal/config"
)

// runChallenge debugs the clearance step. It loads the registration page and
// waits for both cf_clearance sources, and it solves each Turnstile that
// appears. The screenshots and the findings need debug on.
func runChallenge(ctx context.Context, logger *slog.Logger, cfg *config.Config) error { // coverage-ignore: needs a live Chrome
	override, err := resolveUserAgent(ctx, logger, cfg)
	if err != nil {
		return err
	}

	accounts, err := openStore(ctx, logger, cfg)
	if err != nil {
		return err
	}
	proxyMgr, err := runProxies(ctx, logger, cfg, accounts)
	// The challenge saves nothing, so the store closes once the proxies are read.
	if err := errors.Join(err, accounts.Close()); err != nil {
		return err
	}

	p, release, err := proxyMgr.Next(ctx)
	if err != nil {
		return fmt.Errorf("waiting for a proxy: %w", err)
	}
	defer release()
	if !p.IsZero() {
		logger.Info("using proxy", "proxy", p.HostPort())
	}

	// Create the pool of browser cache dirs for the challenge run.
	cache, err := browser.NewCacheManager("", logger)
	if err != nil {
		return fmt.Errorf("initializing browser cache: %w", err)
	}

	// Start the forward proxy, which applies the domain allowlist.
	fwd, chromeProxy, err := startForwardProxy(ctx, logger, p)
	if err != nil {
		return err
	}
	if fwd != nil {
		defer fwd.Stop()
	}

	r := &runner{cfg: cfg, cache: cache, override: override}
	return r.withBrowser(ctx, logger, chromeProxy, nil, func(ctx context.Context, b *browserPhase) error {
		return inspectRegistrationFlow(ctx, logger, cfg, b)
	})
}

// inspectRegistrationFlow drives the browser through the clearance step and
// logs what it finds at DEBUG. It keeps the browser open for 10 seconds at
// the end.
func inspectRegistrationFlow(ctx context.Context, logger *slog.Logger, cfg *config.Config, b *browserPhase) error { // coverage-ignore: needs a live Chrome
	c := b.client.CDP
	// Block unnecessary resources (images, stylesheets, analytics) via
	// Network.setBlockedURLs. Domain filtering is handled by the forward
	// proxy. No Fetch domain needed — preserves Chrome's disk cache.
	if err := browser.SetBlockedURLs(ctx, c, logger); err != nil {
		return err
	}

	gate, err := browser.NewClearanceGate(ctx, c, logger, challengePageTitle)
	if err != nil {
		return fmt.Errorf("starting the clearance gate: %w", err)
	}
	defer gate.Close()

	if err := navigateToRegistration(ctx, c, logger, nil); err != nil {
		return err
	}

	// Wait for Cloudflare challenge to render (if present).
	// Ignore errors — if no Turnstile appears, the screenshot still captures
	// the current page state for diagnostics.
	_ = browser.WaitForJS(ctx, c, browser.TurnstileWaitJS, 3*time.Second)

	// Log page URL and title for diagnostics.
	browser.LogPageState(ctx, c, logger, "after initial load")

	// Screenshot: after page load (debug only).
	browser.SaveDebugScreenshot(ctx, c, logger, cfg.Debug, "01_page_loaded")

	if err := awaitClearance(ctx, logger, cfg, gate); err != nil {
		browser.SaveDebugScreenshot(ctx, c, logger, cfg.Debug, "02_cookies_timeout")
		return err
	}

	logPageRequests(ctx, c, logger)
	browser.LogCookies(ctx, c, logger)

	// Screenshot: after cookies obtained (debug only).
	browser.SaveDebugScreenshot(ctx, c, logger, cfg.Debug, "02_cookies_obtained")

	// Keep Chrome open for inspection.
	logger.Debug("clearance flow complete, keeping browser open for 10 seconds")
	holdBrowserOpen(ctx, 10*time.Second)

	logger.Debug("shutting down")
	return nil
}

// logPageRequests logs the jagex.com requests that the page made.
func logPageRequests(ctx context.Context, c *cdp.Client, logger *slog.Logger) {
	// Wait for page API calls to complete before capturing performance entries.
	// Poll until two consecutive checks see the same count of jagex.com
	// resource entries. WaitForJS stops the poll after 5 seconds.
	perfStableJS := `(function() {
		var n = performance.getEntriesByType('resource').filter(function(e) { return e.name.includes('jagex.com'); }).length;
		if (window._pn === n && window._pn !== undefined) return true;
		window._pn = n;
		return false;
	})()`
	if err := browser.WaitForJS(ctx, c, perfStableJS, 5*time.Second); err != nil {
		logger.Warn("waiting for page API calls to settle", "error", err)
	}

	// Capture what API requests the page made via performance entries.
	var requestsJSON string
	requestsJS := `JSON.stringify(
		performance.getEntriesByType('resource')
			.filter(e => e.name.includes('jagex.com'))
			.map(e => ({name: e.name, type: e.initiatorType}))
	)`
	if err := browser.EvalJS(ctx, c, requestsJS, &requestsJSON); err != nil {
		logger.Warn("failed to get performance entries", "error", err)
	} else {
		logger.Debug("page network requests", "data", requestsJSON)
	}
}

// holdBrowserOpen waits for d, or until ctx ends, so the browser stays open
// for inspection.
func holdBrowserOpen(ctx context.Context, d time.Duration) {
	inspectTimer := time.NewTimer(d)
	defer inspectTimer.Stop()
	select {
	case <-inspectTimer.C:
	case <-ctx.Done():
	}
}
