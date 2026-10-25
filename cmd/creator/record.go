package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"jagex_account_creator/internal/account"
	"jagex_account_creator/internal/browser"
	"jagex_account_creator/internal/email"
	"jagex_account_creator/internal/jagexapi"
	"jagex_account_creator/internal/proxy"
	"jagex_account_creator/internal/store"
)

// attemptRecord holds the measurements of one attempt.
type attemptRecord struct {
	proxy                string
	metered              bool // false for a direct connection, which has no forward proxy
	browserBytesSent     int64
	browserBytesReceived int64
	httpBytesSent        int64
	httpBytesReceived    int64
	total                time.Duration
	steps                *stepTracker
}

// stopAndCount stops both forward proxies and adds their final byte counts to r.
func (r *attemptRecord) stopAndCount(browserFwd, httpFwd *proxy.ForwardProxy) {
	if browserFwd != nil {
		browserFwd.Stop()
		r.metered = true
		r.browserBytesSent, r.browserBytesReceived = browserFwd.Bytes()
	}
	if httpFwd != nil {
		httpFwd.Stop()
		r.httpBytesSent, r.httpBytesReceived = httpFwd.Bytes()
	}
}

// attrs returns the log attributes that the success and the failure lines share.
func (r *attemptRecord) attrs() []slog.Attr {
	attrs := []slog.Attr{slog.String("proxy", r.proxy)}
	if r.metered {
		attrs = append(attrs,
			slog.Int64("browser_bytes_sent", r.browserBytesSent),
			slog.Int64("browser_bytes_received", r.browserBytesReceived),
			slog.Int64("http_bytes_sent", r.httpBytesSent),
			slog.Int64("http_bytes_received", r.httpBytesReceived),
		)
	}
	return append(attrs, slog.Duration("duration", r.total.Round(time.Millisecond)), r.steps.group())
}

// attemptResult is the outcome of one attempt: its record, its account, its
// error and the category of that error. The record line, the run history and
// the run state read it.
type attemptResult struct {
	rec      *attemptRecord
	acct     *account.Account
	err      error
	category string // empty on success
}

// newAttemptResult returns the result of an attempt that ended with acct and
// err. ctx is the context after the write, so the category sees a failed save.
func newAttemptResult(ctx context.Context, rec *attemptRecord, acct *account.Account, err error) attemptResult {
	res := attemptResult{rec: rec, acct: acct, err: err}
	if err != nil {
		res.category = failureCategory(ctx, err)
	}
	return res
}

// saved reports whether the account of the attempt is in the account store.
func (r *attemptResult) saved() bool {
	return r.acct != nil && !errors.Is(r.err, errSaveFailed)
}

// logAttempt logs the record of res: "account created" on success or
// "account creation failed" on failure. A failure line names the account when
// the attempt has one: an unconfirmed account or a failed save.
func logAttempt(ctx context.Context, logger *slog.Logger, res *attemptResult) {
	rec, acct := res.rec, res.acct
	if res.err != nil {
		attrs := []slog.Attr{
			slog.String("outcome", "failure"),
			slog.String("category", res.category),
			slog.String("error", res.err.Error()),
		}
		if acct != nil {
			attrs = append(attrs, accountAttrs(acct)...)
		}
		if s, ok := rec.steps.failedStep(); ok {
			attrs = append(attrs, slog.String("failed_step", s.name), slog.String("failed_phase", s.phase))
		}
		logger.LogAttrs(ctx, slog.LevelError, "account creation failed", slices.Concat(attrs, rec.attrs())...)
		return
	}
	attrs := slices.Concat(
		[]slog.Attr{slog.String("outcome", "success")},
		accountAttrs(acct),
		[]slog.Attr{slog.Bool("2fa", acct.TOTPSecret != "")},
	)
	logger.LogAttrs(ctx, slog.LevelInfo, "account created", slices.Concat(attrs, rec.attrs())...)
}

// accountAttrs returns the email and the username of acct, and unconfirmed=true
// when Jagex did not confirm the last change of acct.
func accountAttrs(acct *account.Account) []slog.Attr {
	attrs := []slog.Attr{slog.String("email", acct.Email), slog.String("username", acct.Username)}
	if acct.Unconfirmed {
		attrs = append(attrs, slog.Bool("unconfirmed", true))
	}
	return attrs
}

// logAttemptStart logs the start of an attempt through p. The zero Proxy is a
// direct connection.
func logAttemptStart(logger *slog.Logger, p proxy.Proxy) {
	if !p.IsZero() {
		logger.Info("starting account creation", "proxy", p.HostPort())
	} else {
		logger.Info("starting account creation (direct connection)")
	}
}

// errSaveFailed matches an error of an account that Jagex created, or may have
// created, but the account store did not take.
var errSaveFailed = errors.New("account not saved")

// errAccountWritten matches the error of a failed attempt whose account is in
// the account store.
var errAccountWritten = errors.New("account written")

// writeAccount adds the per-phase byte counts of rec to acct and saves acct to
// accounts. A direct connection has no counts, so the record omits them.
func writeAccount(ctx context.Context, accounts *store.Store, acct *account.Account, rec *attemptRecord) error {
	acct.BrowserBytesSent = rec.browserBytesSent
	acct.BrowserBytesReceived = rec.browserBytesReceived
	acct.HTTPBytesSent = rec.httpBytesSent
	acct.HTTPBytesReceived = rec.httpBytesReceived
	if err := accounts.WriteAccount(ctx, acct); err != nil {
		return fmt.Errorf("%w: %w", errSaveFailed, err)
	}
	return nil
}

// failureCategory returns the category of a failed attempt, so that an operator
// can tell the failure modes apart. A failed save comes first: the end of the
// attempt context does not end the save, so that end is never its cause. A
// failed save of an unconfirmed account also carries the cause of the attempt.
// Then the end of the attempt context: a cancel is "cancelled" and its deadline
// is "account_timeout". Then the sentinel in err decides.
func failureCategory(ctx context.Context, err error) string {
	switch {
	case errors.Is(err, errSaveFailed):
		return "save_failed"
	case errors.Is(err, context.Canceled), errors.Is(ctx.Err(), context.Canceled):
		return "cancelled"
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "account_timeout"
	case errors.Is(err, jagexapi.ErrRateLimited):
		return "rate_limited"
	case errors.Is(err, browser.ErrProxyConnection):
		return "proxy_error"
	case errors.Is(err, browser.ErrUserAgentOverride):
		return "ua_override"
	case errors.Is(err, browser.ErrChallengeTimeout):
		return "challenge_timeout"
	case errors.Is(err, jagexapi.ErrRegistrationFailed):
		return "registration_failed"
	case errors.Is(err, email.ErrEmailLogin):
		return "email_login"
	case errors.Is(err, email.ErrEmailTimeout):
		return "email_timeout"
	default:
		return "unknown"
	}
}
