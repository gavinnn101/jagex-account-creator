package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/sardanioss/httpcloak/client"

	"jagex_account_creator/internal/account"
	"jagex_account_creator/internal/jagexapi"
	"jagex_account_creator/internal/useragent"
)

// creationTimeout bounds the password POST and the wait for the account together.
const creationTimeout = 30 * time.Second

// enrollTimeout bounds the 2FA enrollment.
const enrollTimeout = 30 * time.Second

// registerOverHTTP opens the oauth_start step. It imports sessionCookies into
// httpClient, joins fetch, starts the registration and registers the account.
// It adds 2FA when the config enables it. From the password POST on, the work
// ignores the end of ctx and stops only on the stop-at-once signal. It returns
// the account from the password POST on, unless Jagex refused the POST, for the caller to write.
func (a *attempt) registerOverHTTP(ctx context.Context, httpClient *jagexapi.Client, fetch *loginLinkFetch, sessionCookies []client.Cookie) (*account.Account, error) {
	a.steps.begin("oauth_start", phaseHTTP)
	httpClient.ImportCookies(sessionCookies)

	// httpcloak's pool has no single-flight dial, so the first hop must wait for the fetch's connection or it opens a second one.
	// Limitation: a failed fetch fails the attempt after a solved challenge. No second fetch runs, because doWithRetry already retried.
	joinStart := time.Now()
	loginLink, err := fetch.wait()
	if err != nil {
		return nil, fmt.Errorf("starting registration: %w", err)
	}
	a.logger.Debug("login link fetched during the challenge", "fetch", fetch.took.Round(time.Millisecond), "join_wait", time.Since(joinStart).Round(time.Millisecond))
	regClient, err := jagexapi.StartRegistration(ctx, httpClient, a.logger, loginLink)
	if err != nil {
		return nil, fmt.Errorf("starting registration: %w", err)
	}

	id := a.generateIdentity()

	if err := a.submitRegistration(ctx, regClient, id); err != nil {
		return nil, err
	}

	tail, cancel := stopNowContext(ctx, a.run.stopNow)
	defer cancel()
	acct, err := a.createRemoteAccount(tail, regClient, id)
	if err != nil {
		return acct, err
	}

	enrollCtx, enrollCancel := context.WithTimeout(tail, enrollTimeout)
	defer enrollCancel()
	a.enrollIfEnabled(enrollCtx, regClient, acct)
	return acct, nil
}

// errPasswordRefused matches the error of a password POST that Jagex refused.
// Such an attempt has no account, but its work ran outside the attempt context.
var errPasswordRefused = errors.New("password refused")

// createRemoteAccount sends the password and waits until Jagex has created the
// account, both under creationTimeout. A refused POST returns no account and an
// error that matches errPasswordRefused. Any other error from the POST on
// returns the account with Unconfirmed set, because Jagex can have created it.
func (a *attempt) createRemoteAccount(ctx context.Context, regClient registrationSubmitter, id identity) (*account.Account, error) {
	a.steps.begin("submit_password", phaseHTTP)
	createCtx, cancel := context.WithTimeout(ctx, creationTimeout)
	defer cancel()
	acct := &account.Account{
		Email:     id.email,
		Username:  id.username,
		Password:  id.password,
		CreatedAt: time.Now().UTC(),
		Proxy:     a.p.HostPort(),
	}

	// The account can exist remotely from this POST on, and its password lives only in this process.
	err := regClient.SubmitPassword(createCtx, id.username, id.password)
	// Limitation: a 4xx or 429 on the password POST counts as a refusal. No run checked such an account. It fails toward a lost account.
	if jagexapi.Refused(err) {
		return nil, markedError{err: err, mark: errPasswordRefused}
	}
	if err == nil {
		a.steps.begin("wait_account_creation", phaseHTTP)
		a.logger.Debug("waiting for account creation to complete")
		err = regClient.WaitForAccountCreation(createCtx)
	}
	// Limitation: a FAILED state in the wait after a creating answer still writes the account unconfirmed. No run saw one. It fails toward an extra line.
	if err != nil {
		acct.Unconfirmed = true
		return acct, err
	}
	a.logger.Debug("registration complete")

	// Completing registration is what the proxy earned.
	a.run.proxyMgr.ReportSuccess(a.p)
	return acct, nil
}

// loginLinkFetch is a login-link request that runs in the background, so that
// it overlaps the challenge solve.
type loginLinkFetch struct {
	cancel context.CancelFunc
	done   chan struct{} // closed when the fetch returns
	link   *url.URL
	err    error
	took   time.Duration // the duration of the fetch
}

// startLoginLinkFetch runs fetch in the background on a child context of ctx.
// The caller must call stop.
func startLoginLinkFetch(ctx context.Context, fetch func(ctx context.Context) (*url.URL, error)) *loginLinkFetch {
	fetchCtx, cancel := context.WithCancel(ctx)
	f := &loginLinkFetch{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(f.done)
		start := time.Now()
		f.link, f.err = fetch(fetchCtx)
		f.took = time.Since(start)
	}()
	return f
}

// wait returns the result of the fetch once it is done. Each call returns the
// same result.
func (f *loginLinkFetch) wait() (*url.URL, error) {
	<-f.done
	return f.link, f.err
}

// stop cancels the fetch and waits until it returns.
func (f *loginLinkFetch) stop() {
	f.cancel()
	<-f.done
}

// browserSession is the state that the HTTP client takes over from the
// browser, so that its requests match the browser session.
type browserSession struct {
	cookies   []client.Cookie
	userAgent string
	hints     useragent.ClientHints
}

// newHTTPClient returns an HTTP client that connects through httpProxy and
// holds the browser session. The caller must close it.
func (a *attempt) newHTTPClient(session browserSession, httpProxy string) *jagexapi.Client {
	// Cloudflare compares the UA and the hints of the API requests with the browser's. NewClient logs the hints.
	a.logger.Debug("browser user agent", "ua", session.userAgent)
	httpClient := jagexapi.NewClient(a.logger, session.userAgent, session.hints, httpProxy)
	httpClient.ImportCookies(session.cookies)
	return httpClient
}

// identity is the generated data of a new account.
type identity struct {
	email    string
	dob      string
	username string
	password string
}

// generateIdentity generates the email address, the date of birth and the
// credentials of a new account.
func (a *attempt) generateIdentity() identity {
	emailAddr := account.GenerateEmail(a.run.cfg.Email.IMAPDomains)
	a.logger.Debug("generated email", "email", emailAddr)

	dob := account.GenerateDOB()
	a.logger.Debug("generated DOB", "dob", dob)

	// Generate username + password before the email wait so the data is
	// ready as soon as the verification code arrives.
	username := account.GenerateName()
	password := account.GeneratePassword(a.run.cfg.Account.PasswordLength)
	a.logger.Debug("generated credentials", "username", username)
	return identity{email: emailAddr, dob: dob, username: username, password: password}
}

// registrationSubmitter is the part of the registration client that
// submitRegistration and createRemoteAccount call.
type registrationSubmitter interface {
	InitRegistration(ctx context.Context) error
	SubmitInitialRegistration(ctx context.Context, email, dob string) error
	SubmitVerificationCode(ctx context.Context, code string) error
	ValidateDisplayName(ctx context.Context, displayName string) error
	SubmitPassword(ctx context.Context, displayName, password string) error
	WaitForAccountCreation(ctx context.Context) error
}

// submitRegistration runs the HTTP registration flow up to the password step,
// in the order of the page's screens. It fails when ctx has ended, so the
// caller sends no password after a stop or the account timeout.
func (a *attempt) submitRegistration(ctx context.Context, regClient registrationSubmitter, id identity) error {
	// Record time just before registration. The mail search matches from the UTC
	// date of this time on.
	registerTime := time.Now().UTC().Add(-5 * time.Second)

	// Initialize the registration state machine (replicates the XHR the page makes on load).
	a.steps.begin("init_registration", phaseHTTP)
	a.logger.Debug("initializing registration session")
	if err := regClient.InitRegistration(ctx); err != nil {
		return err
	}

	// Submit initial registration (email + DOB).
	a.steps.begin("submit_registration", phaseHTTP)
	a.logger.Debug("submitting initial registration")
	if err := regClient.SubmitInitialRegistration(ctx, id.email, id.dob); err != nil {
		return err
	}

	a.steps.begin("email_wait", phaseHTTP)
	a.logger.Debug("waiting for verification code")
	code, err := a.emailProvider.WaitForVerificationCode(ctx, id.email, registerTime, 2*time.Minute)
	if err != nil {
		return err
	}
	a.logger.Debug("verification code received")

	a.steps.begin("submit_code", phaseHTTP)
	if err := regClient.SubmitVerificationCode(ctx, code); err != nil {
		return err
	}

	a.steps.begin("validate_name", phaseHTTP)
	if err := regClient.ValidateDisplayName(ctx, id.username); err != nil {
		return err
	}
	a.logger.Debug("display name validated", "username", id.username)

	// Limitation: a stop that lands between this check and the POST still sends the password. That fails toward one more created and written account.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("stopping before the password: %w", err)
	}
	return nil
}
