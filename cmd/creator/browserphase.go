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
	"jagex_account_creator/internal/jagexapi"
)

// challengePageTitle is the title of Jagex's own Cloudflare challenge page.
const challengePageTitle = "Security | Jagex"

// enableDomains enables the Page and Network domains.
func enableDomains(ctx context.Context, c *cdp.Client) error { // coverage-ignore: needs a live Chrome
	if err := c.Page.Enable(ctx); err != nil {
		return fmt.Errorf("enabling page domain: %w", err)
	}
	if err := c.Network.Enable(ctx, nil); err != nil {
		return fmt.Errorf("enabling network domain: %w", err)
	}
	return nil
}

// browserPhase is one launched Chrome and the CDP set-up that withBrowser made on it.
type browserPhase struct {
	launcher *browser.Launcher
	client   *browser.Client
	tracker  *browser.NetworkTracker // nil unless debug is on
	attacher *browser.TargetAttacher // nil when the child targets need no set-up
	// Limitation: finished is not goroutine-safe. Every finishTargets call runs on the goroutine of withBrowser's fn.
	finished bool
}

// finishTargets stops the set-up of new child targets and logs the DEBUG
// traffic breakdown. The first call does the work, and a later call does
// nothing. The withBrowser defer calls it, so a failed phase also logs the
// breakdown. The success path calls it early, so the breakdown prints before the HTTP phase.
func (b *browserPhase) finishTargets(logger *slog.Logger) {
	if b.finished {
		return
	}
	b.finished = true
	// An attach in progress subscribes the tracker, so the attacher closes first.
	b.attacher.Close()
	b.tracker.Close()
	b.tracker.LogSummary(logger)
}

// withBrowser launches Chrome through chromeProxy and connects to it over CDP.
// It enables the Page and Network domains, sends the run's UA override to the
// page and starts the child target set-up. Then it runs fn on a context that a
// child without the override cancels. A run with no override keeps Chrome's own
// UA. It opens the chrome_launch and cdp_connect steps on steps. A nil steps
// opens no step. It closes the CDP connection and kills Chrome before it returns.
// The blocked URLs, the clearance gate, the cookie clear and the forward proxy
// start stay with the caller. Their order differs between the two modes.
func (r *runner) withBrowser(
	ctx context.Context,
	logger *slog.Logger,
	chromeProxy string,
	steps *stepTracker,
	fn func(ctx context.Context, b *browserPhase) error,
) error { // coverage-ignore: needs a live Chrome
	browserCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	steps.begin("chrome_launch", phaseBrowser)
	launcher := browser.NewLauncher(logger, r.cache)
	defer launcher.Kill()
	if err := launcher.Launch(browserCtx, *r.cfg.Chrome.Headless, chromeProxy); err != nil {
		return err
	}

	steps.begin("cdp_connect", phaseBrowser)
	cdpClient, err := browser.NewClient(browserCtx, launcher.WebSocketURL(), logger)
	if err != nil {
		return fmt.Errorf("connecting to CDP: %w", err)
	}
	defer func() { _ = cdpClient.Close() }()

	if err := enableDomains(browserCtx, cdpClient.CDP); err != nil {
		return err
	}
	b := &browserPhase{launcher: launcher, client: cdpClient}
	defer b.finishTargets(logger)
	if err := b.setUpTargets(browserCtx, logger, r.cfg.Debug, r.override, cancel); err != nil {
		return err
	}
	// The cause is read before the deferred closes and the deferred cancel run.
	return browserPhaseError(browserCtx, fn(browserCtx, b))
}

// setUpTargets sends override to the page before its first navigation. Then it
// starts the attacher with up to two set-up steps for each child target. The
// steps are the override and the subscription of the tracker that debug starts.
// With neither step, no attacher starts. A child that fails the override calls fail.
func (b *browserPhase) setUpTargets(ctx context.Context, logger *slog.Logger, debug bool, override *browser.UserAgentOverride, fail context.CancelCauseFunc) error { // coverage-ignore: needs a live Chrome
	if override != nil {
		if err := b.client.SetUserAgentOverride(ctx, override); err != nil {
			return err
		}
	}
	// The CDP tracker feeds only the DEBUG breakdown. The forward proxy counts the real bytes.
	if debug {
		t, err := browser.NewNetworkTracker(ctx, b.client.CDP)
		if err != nil {
			return fmt.Errorf("starting network tracker: %w", err)
		}
		b.tracker = t
	}
	if override == nil && b.tracker == nil {
		return nil
	}
	b.attacher = browser.NewTargetAttacher(logger, override, b.tracker, fail)
	if err := b.attacher.Start(ctx, b.client.CDP); err != nil {
		return fmt.Errorf("starting the target attacher: %w", err)
	}
	return nil
}

// browserPhaseError returns the cause of ctx when a child target failed the UA
// override, whatever fn returned. A wait that the cancel ended reports only
// context.Canceled. A hold that the cancel ended returns nil. A parent cancel
// has a cause of its own, so fn's result stays.
func browserPhaseError(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); errors.Is(cause, browser.ErrUserAgentOverride) {
		return fmt.Errorf("stopping the browser phase: %w", cause)
	}
	return err
}

// navigateToRegistration loads the registration page. A load timeout only
// warns. On a proxy connection failure, a non-nil onProxyFailure gets the error,
// and its result is the error of navigateToRegistration.
func navigateToRegistration(ctx context.Context, c *cdp.Client, logger *slog.Logger, onProxyFailure func(error) error) error {
	logger.Debug("navigating to registration page")
	if err := browser.NavigateAndLoad(ctx, c, jagexapi.ManageRegistrationURL, 30*time.Second); err != nil {
		if errors.Is(err, browser.ErrLoadTimeout) {
			logger.Warn("page load timed out, continuing anyway", "url", jagexapi.ManageRegistrationURL)
			return nil
		}
		if onProxyFailure != nil && errors.Is(err, browser.ErrProxyConnection) {
			err = onProxyFailure(err)
		}
		return err
	}
	return nil
}

// awaitClearance waits until gate sees both cf_clearance sources.
func awaitClearance(ctx context.Context, logger *slog.Logger, cfg *config.Config, gate *browser.ClearanceGate) error { // coverage-ignore: needs a live Chrome
	if err := gate.Wait(ctx, cfg.Chrome.Timeout, cfg.Debug); err != nil {
		return fmt.Errorf("waiting for clearance: %w", err)
	}
	logger.Debug("clearance earned")
	return nil
}
