package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"

	"github.com/mafredri/cdp"
	"github.com/sardanioss/httpcloak/client"

	"jagex_account_creator/internal/account"
	"jagex_account_creator/internal/browser"
	"jagex_account_creator/internal/cookies"
	"jagex_account_creator/internal/email"
	"jagex_account_creator/internal/jagexapi"
	"jagex_account_creator/internal/proxy"
)

// attempt holds the values of one account creation attempt that are fixed once
// its proxy is picked. The values that the run shares come through run. The
// browser and HTTP handles stay parameters, because the deferred calls of the
// frame that creates each handle own its lifetime.
type attempt struct {
	run           *runner
	logger        *slog.Logger
	worker        workerState
	emailProvider email.Provider
	p             proxy.Proxy
	steps         *stepTracker
	releaseSlot   func()
}

// newAttempt sets up an attempt of the worker ws through p, which holds slot.
// The step clock starts at the slot wait, and each step that it opens reaches
// the run state. It opens the proxy_check step and logs the start.
func (r *runner) newAttempt(logger *slog.Logger, ws workerState, emailProvider email.Provider, p proxy.Proxy, slot browserSlot) *attempt {
	steps := newStepTracker(slot.waitStart, stepSlotWait, phaseBrowser)
	steps.onBegin = ws.stepStarted
	steps.begin("proxy_check", phaseBrowser)
	logAttemptStart(logger, p)
	return &attempt{
		run:           r,
		logger:        logger,
		worker:        ws,
		emailProvider: emailProvider,
		p:             p,
		steps:         steps,
		releaseSlot:   slot.release,
	}
}

// createAccount makes one attempt of the worker ws through p. It owns the two
// forward proxies of the attempt. It checks the upstream through the browser
// forward proxy before it starts the HTTP forward proxy. It stops them after the
// browser and the HTTP client are gone, then it writes the account and logs the
// attempt record.
func (r *runner) createAccount(ctx context.Context, logger *slog.Logger, ws workerState, emailProvider email.Provider, p proxy.Proxy, slot browserSlot) error { // coverage-ignore: needs a live Chrome
	a := r.newAttempt(logger, ws, emailProvider, p, slot)

	// Each phase gets its own forward proxy, so each phase gets its own byte
	// count. A forward proxy also applies the domain allowlist and the upstream auth.
	browserFwd, chromeProxy, err := startForwardProxy(ctx, logger.With("phase", phaseBrowser), p)
	if err != nil {
		return a.finish(ctx, nil, err, nil, nil)
	}
	if err = a.checkUpstream(ctx, browserFwd); err != nil {
		// A failed check has no byte fields in its record, so checkUpstream
		// has stopped browserFwd and finish gets no forward proxy.
		return a.finish(ctx, nil, err, nil, nil)
	}
	httpFwd, _, err := startForwardProxy(ctx, logger.With("phase", phaseHTTP), p)
	if err != nil {
		return a.finish(ctx, nil, err, browserFwd, nil)
	}

	acct, err := a.launchAndRegister(ctx, chromeProxy, forwardProxyURL(httpFwd))
	return a.finish(ctx, acct, err, browserFwd, httpFwd)
}

// launchAndRegister launches Chrome through chromeProxy and registers the
// account. The HTTP client connects through httpProxy. It returns the account
// once its password is sent, also with an error, unless Jagex refused the password.
func (a *attempt) launchAndRegister(ctx context.Context, chromeProxy, httpProxy string) (*account.Account, error) { // coverage-ignore: needs a live Chrome
	var acct *account.Account
	err := a.run.withBrowser(ctx, a.logger, chromeProxy, a.steps, func(ctx context.Context, b *browserPhase) error {
		var err error
		acct, err = a.registerInBrowser(ctx, b, httpProxy)
		return err
	})
	return acct, err
}

// finish ends the attempt. It stops both forward proxies and reads their final
// counts. Then it writes acct whenever it is set, also after an error. It logs
// the one record of the attempt, saves its row to the run history and counts it
// in the run state. A failed account write joins the cause of the attempt.
// After a write, the error of a failed attempt also matches errAccountWritten.
func (a *attempt) finish(ctx context.Context, acct *account.Account, err error, browserFwd, httpFwd *proxy.ForwardProxy) error {
	if err != nil {
		a.steps.fail()
	}
	if errors.Is(err, jagexapi.ErrRateLimited) {
		err = a.coolProxy("rate limited", err)
	}
	rec := attemptRecord{proxy: a.p.HostPort(), total: a.steps.finish(), steps: a.steps}
	rec.stopAndCount(browserFwd, httpFwd)
	if acct != nil || errors.Is(err, errPasswordRefused) {
		// The work from the password POST on does not run under the attempt context, so the end of the attempt context is never its cause.
		ctx = context.WithoutCancel(ctx)
	}
	if acct != nil {
		if saveErr := writeAccount(ctx, a.run.history.accounts, acct, &rec); saveErr != nil {
			err = joinSaveError(saveErr, err)
		} else if err != nil {
			err = markedError{err: err, mark: errAccountWritten}
		}
	}
	res := newAttemptResult(ctx, &rec, acct, err)
	logAttempt(ctx, a.logger, &res)
	a.saveAttempt(ctx, &res)
	a.worker.attemptFinished(&res)
	return err
}

// joinSaveError puts saveErr in front of cause, so that both match and the
// text stays on one line.
func joinSaveError(saveErr, cause error) error {
	if cause == nil {
		return saveErr
	}
	return fmt.Errorf("%w: %w", saveErr, cause)
}

// registerInBrowser earns the Cloudflare clearance in the browser while the
// HTTP client fetches the login link. Then it reads the browser cookies, shuts
// Chrome down in the background and registers the account over httpProxy. It
// waits for the shutdown before it returns.
func (a *attempt) registerInBrowser(ctx context.Context, b *browserPhase, httpProxy string) (*account.Account, error) { // coverage-ignore: needs a live Chrome
	c := b.client.CDP

	gate, err := browser.NewClearanceGate(ctx, c, a.logger, challengePageTitle)
	if err != nil {
		return nil, fmt.Errorf("starting the clearance gate: %w", err)
	}
	defer gate.Close()

	if err := a.loadRegistrationPage(ctx, c); err != nil {
		return nil, err
	}

	// After the solve, an Evaluate waits for the challenge page's own navigation, so the reads come first.
	userAgent, hints, err := browser.GetUserAgentAndHints(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("getting browser UA+hints: %w", err)
	}
	earlyCookies, err := a.readCookies(ctx, c)
	if err != nil {
		return nil, err
	}
	httpClient := a.newHTTPClient(browserSession{cookies: earlyCookies, userAgent: userAgent, hints: *hints}, httpProxy)
	defer httpClient.Close()

	// Limitation: the fetch carries no cf_clearance. Cloudflare answers it without one, and a refusal fails loud at the join as registration_failed, or as rate_limited on a 429.
	// Limitation: the fetch's connection is idle during the solve. After a drop that the h2 reader sees, the pool dials again. A request that races the drop fails into doWithRetry.
	fetch := startLoginLinkFetch(ctx, func(ctx context.Context) (*url.URL, error) {
		return jagexapi.LoginLink(ctx, httpClient, a.logger)
	})
	// This defer runs before the Close above, so that no Close runs while the fetch's Do is in flight.
	defer fetch.stop()

	a.steps.begin("challenge", phaseBrowser)
	if err := awaitClearance(ctx, a.logger, a.run.cfg, gate); err != nil {
		return nil, err
	}

	a.steps.begin("read_session", phaseBrowser)
	sessionCookies, err := a.readCookies(ctx, c)
	if err != nil {
		return nil, err
	}

	// The browser has no more work. The child set-up stops before the shutdown,
	// because a child that the shutdown closes fails its set-up and cancels ctx.
	b.finishTargets(a.logger)

	shutdownDone := a.shutDownInBackground(ctx, b.launcher, b.client)
	acct, err := a.registerOverHTTP(ctx, httpClient, fetch, sessionCookies)
	a.awaitShutdown(err, shutdownDone)
	return acct, err
}

// shutDownInBackground shuts Chrome down in the background and frees the
// browser slot once Chrome has exited. The end of ctx does not cut the
// graceful exit. The stop-at-once signal kills Chrome. The returned channel
// closes when the shutdown is done.
func (a *attempt) shutDownInBackground(ctx context.Context, launcher *browser.Launcher, cdpClient *browser.Client) <-chan struct{} { // coverage-ignore: needs a live Chrome
	// The graceful exit lets Chrome flush its disk cache while registration runs
	// over HTTP. The deferred Kill and Close of withBrowser are idempotent.
	shutCtx, cancel := stopNowContext(ctx, a.run.stopNow)
	done := make(chan struct{})
	go func() {
		defer cancel()
		launcher.Shutdown(shutCtx, cdpClient.CDP)
		a.releaseSlot() // Chrome has exited, so another attempt may launch its own.
		_ = cdpClient.Close()
		close(done)
	}()
	return done
}

// awaitShutdown waits for done in the browser_shutdown_wait step. An error
// marks the open step as failed first, so the wait never takes the failure.
func (a *attempt) awaitShutdown(err error, done <-chan struct{}) {
	if err != nil {
		a.steps.fail()
	}
	a.steps.begin("browser_shutdown_wait", phaseBrowser)
	<-done
}

// loadRegistrationPage opens the navigate step. It clears the browser cookies
// and loads the registration page, which runs JSD and then starts the OAuth2
// redirects to the challenge.
func (a *attempt) loadRegistrationPage(ctx context.Context, c *cdp.Client) error {
	a.steps.begin("navigate", phaseBrowser)
	if err := browser.ClearBrowserCookies(ctx, c); err != nil {
		return err
	}

	// Block unnecessary resources (images, stylesheets, analytics) via
	// Network.setBlockedURLs. Domain filtering is handled by the forward
	// proxy. No Fetch domain needed — preserves Chrome's disk cache.
	if err := browser.SetBlockedURLs(ctx, c, a.logger); err != nil {
		return err
	}

	return navigateToRegistration(ctx, c, a.logger, func(err error) error {
		return a.coolProxy("page load", err)
	})
}

// readCookies reads the browser cookies in the form that the HTTP client imports.
func (a *attempt) readCookies(ctx context.Context, c *cdp.Client) ([]client.Cookie, error) { // coverage-ignore: needs a live Chrome
	cdpCookies, err := browser.GetAllCookies(ctx, c)
	if err != nil {
		return nil, err
	}
	httpCookies := cookies.CDPToHTTP(cdpCookies)
	a.logger.Debug("cookies extracted", "count", len(httpCookies))
	return httpCookies, nil
}
