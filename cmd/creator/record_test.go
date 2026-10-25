package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jagex_account_creator/internal/account"
	"jagex_account_creator/internal/browser"
	"jagex_account_creator/internal/email"
	"jagex_account_creator/internal/jagexapi"
	"jagex_account_creator/internal/logtest"
	"jagex_account_creator/internal/proxy"
	"jagex_account_creator/internal/store"
)

func TestFailureCategory(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		cancelled    bool // the attempt context is cancelled
		expired      bool // the attempt context is past its deadline
		wantCategory string
	}{
		{
			name:         "challenge timeout",
			err:          fmt.Errorf("waiting for cookies: %w", browser.ErrChallengeTimeout),
			wantCategory: "challenge_timeout",
		},
		{
			name:         "registration failed",
			err:          fmt.Errorf("submitting registration: %w", jagexapi.ErrRegistrationFailed),
			wantCategory: "registration_failed",
		},
		{
			name:         "email timeout",
			err:          fmt.Errorf("waiting for verification: %w", email.ErrEmailTimeout),
			wantCategory: "email_timeout",
		},
		{
			name:         "email login",
			err:          fmt.Errorf("waiting for verification: %w: authentication failed", email.ErrEmailLogin),
			wantCategory: "email_login",
		},
		{
			name:         "per-account timeout wins over email_login",
			err:          fmt.Errorf("waiting for verification: %w: authentication failed", email.ErrEmailLogin),
			expired:      true,
			wantCategory: "account_timeout",
		},
		{
			name:         "rate limited",
			err:          fmt.Errorf("initial registration: %w: status 429", jagexapi.ErrRateLimited),
			wantCategory: "rate_limited",
		},
		{
			name:         "a cancel wins over rate_limited",
			err:          fmt.Errorf("initial registration: %w: status 429", jagexapi.ErrRateLimited),
			cancelled:    true,
			wantCategory: "cancelled",
		},
		{
			name:         "proxy error",
			err:          fmt.Errorf("proxy connectivity check: %w", browser.ErrProxyConnection),
			wantCategory: "proxy_error",
		},
		{
			name:         "UA override failure",
			err:          fmt.Errorf("%w: iframe target T1: enabling Network: target closed", browser.ErrUserAgentOverride),
			wantCategory: "ua_override",
		},
		{
			name:         "unknown error",
			err:          errors.New("something unexpected"),
			wantCategory: "unknown",
		},
		{
			name:         "double-wrapped challenge timeout",
			err:          fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", browser.ErrChallengeTimeout)),
			wantCategory: "challenge_timeout",
		},
		{
			name:         "cancel in the error",
			err:          fmt.Errorf("navigating to registration page: %w", context.Canceled),
			wantCategory: "cancelled",
		},
		{
			name:         "cancelled context",
			err:          errors.New("connection reset"),
			cancelled:    true,
			wantCategory: "cancelled",
		},
		{
			name:         "a cancel wins over a sentinel",
			err:          fmt.Errorf("proxy connectivity check: %w", browser.ErrProxyConnection),
			cancelled:    true,
			wantCategory: "cancelled",
		},
		{
			name:         "per-account timeout",
			err:          fmt.Errorf("waiting: %w", context.DeadlineExceeded),
			expired:      true,
			wantCategory: "account_timeout",
		},
		{
			name:         "per-account timeout in the challenge wait",
			err:          fmt.Errorf("waiting for clearance: %w", browser.ErrChallengeTimeout),
			expired:      true,
			wantCategory: "account_timeout",
		},
		{
			name:         "save failed",
			err:          fmt.Errorf("%w: writing account line: disk full", errSaveFailed),
			wantCategory: "save_failed",
		},
		{
			name:         "save_failed with an expired context",
			err:          fmt.Errorf("%w: writing account line: disk full", errSaveFailed),
			expired:      true,
			wantCategory: "save_failed",
		},
		{
			name:         "save_failed with a cancelled context",
			err:          fmt.Errorf("%w: writing account line: disk full", errSaveFailed),
			cancelled:    true,
			wantCategory: "save_failed",
		},
		{
			name:         "save_failed wins over the joined cancel of an unconfirmed account",
			err:          joinSaveError(fmt.Errorf("%w: writing account line: disk full", errSaveFailed), fmt.Errorf("polling: %w", context.Canceled)),
			wantCategory: "save_failed",
		},
		{
			name:         "an inner deadline stays with its step",
			err:          fmt.Errorf("waiting for account creation: %w", context.DeadlineExceeded),
			wantCategory: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tt.cancelled {
				cancel()
			}
			if tt.expired {
				var cancelDeadline context.CancelFunc
				ctx, cancelDeadline = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancelDeadline()
			}
			if got := failureCategory(ctx, tt.err); got != tt.wantCategory {
				t.Errorf("failureCategory = %q, want %q", got, tt.wantCategory)
			}
		})
	}
}

func TestLogAttemptStart(t *testing.T) {
	t.Run("direct connection", func(t *testing.T) {
		var buf bytes.Buffer
		logAttemptStart(logtest.New(&buf), proxy.Proxy{})
		if !strings.Contains(buf.String(), `msg="starting account creation (direct connection)"`) {
			t.Errorf("log has no direct connection line:\n%s", buf.String())
		}
	})

	t.Run("proxy", func(t *testing.T) {
		var buf bytes.Buffer
		logAttemptStart(logtest.New(&buf), testProxy())
		if !strings.Contains(buf.String(), `msg="starting account creation" proxy=`+testProxyAddr) {
			t.Errorf("log has no proxy line:\n%s", buf.String())
		}
	})
}

// newTestStore opens a new account store in a temp dir. Its cleanup closes the
// store before the dir is removed, because Windows cannot remove an open file.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(t.Context(), filepath.Join(dir, "creator.db"), filepath.Join(dir, "accounts.jsonl"), filepath.Join(dir, "proxies.txt"), logtest.New(io.Discard))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

// readRecords returns the JSONL export of s.
func readRecords(t *testing.T, s *store.Store) string {
	t.Helper()
	var buf bytes.Buffer
	if err := s.ExportJSONL(t.Context(), &buf, store.AccountFilter{}); err != nil {
		t.Fatalf("ExportJSONL: %v", err)
	}
	return buf.String()
}

func TestWriteAccount(t *testing.T) {
	tests := []struct {
		name string
		rec  attemptRecord
		want string // the JSON after "created_at"
	}{
		{
			name: "metered",
			rec:  attemptRecord{metered: true, browserBytesSent: 10, browserBytesReceived: 20000, httpBytesSent: 100, httpBytesReceived: 1000},
			want: `"browser_bytes_sent":10,"browser_bytes_received":20000,"http_bytes_sent":100,"http_bytes_received":1000}`,
		},
		{name: "direct connection", want: `"created_at":"0001-01-01T00:00:00Z"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			acct := &account.Account{Email: "user@example.com", Username: "user"}

			if err := writeAccount(t.Context(), s, acct, &tt.rec); err != nil {
				t.Fatalf("writeAccount: %v", err)
			}

			if got := strings.TrimSpace(readRecords(t, s)); !strings.HasSuffix(got, tt.want) {
				t.Errorf("record = %s, want the suffix %s", got, tt.want)
			}
		})
	}

	t.Run("closed store", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}

		err := writeAccount(t.Context(), s, &account.Account{Email: "user@example.com"}, &attemptRecord{})
		// database/sql keeps its closed error unexported, so the text names it.
		const want = "account not saved: saving account: sql: database is closed"
		if !errors.Is(err, errSaveFailed) || err.Error() != want {
			t.Fatalf("error = %v, want errSaveFailed with the text %q", err, want)
		}
	})
}

func TestLogAttempt(t *testing.T) {
	const counts = "proxy=203.0.113.1:8080 browser_bytes_sent=10 browser_bytes_received=20 http_bytes_sent=30 http_bytes_received=40"
	metered := func(total time.Duration, failed int, steps ...step) *attemptRecord {
		return &attemptRecord{
			proxy: "203.0.113.1:8080", metered: true,
			browserBytesSent: 10, browserBytesReceived: 20, httpBytesSent: 30, httpBytesReceived: 40,
			total: total, steps: &stepTracker{steps: steps, failed: failed},
		}
	}
	tests := []struct {
		name    string
		rec     *attemptRecord
		acct    *account.Account
		err     error
		expired bool // the attempt context is past its deadline
		want    string
	}{
		{
			name: "success",
			rec:  metered(8*time.Second, -1, step{"proxy_check", phaseBrowser, time.Second}, step{"submit_password", phaseHTTP, 7 * time.Second}),
			acct: &account.Account{Email: "user@example.com", Username: "user", TOTPSecret: "S"},
			want: `level=INFO msg="account created" outcome=success email=user@example.com username=user 2fa=true ` + counts +
				` duration=8s steps.proxy_check=1s steps.submit_password=7s`,
		},
		{
			name: "browser phase failure",
			rec:  metered(8*time.Second, 1, step{"proxy_check", phaseBrowser, 3 * time.Second}, step{"challenge", phaseBrowser, 5 * time.Second}),
			err:  fmt.Errorf("waiting for clearance: %w", browser.ErrChallengeTimeout),
			want: `level=ERROR msg="account creation failed" outcome=failure category=challenge_timeout ` +
				`error="waiting for clearance: cloudflare challenge timed out" failed_step=challenge failed_phase=browser ` + counts +
				` duration=8s steps.proxy_check=3s steps.challenge=5s`,
		},
		{
			name: "http phase failure",
			rec: metered(10*time.Second, 1,
				step{"proxy_check", phaseBrowser, 4 * time.Second}, step{"email_wait", phaseHTTP, 5 * time.Second}, step{"browser_shutdown_wait", phaseBrowser, time.Second}),
			err: fmt.Errorf("waiting for verification code: %w", email.ErrEmailTimeout),
			want: `level=ERROR msg="account creation failed" outcome=failure category=email_timeout ` +
				`error="waiting for verification code: email verification timed out" failed_step=email_wait failed_phase=http ` + counts +
				` duration=10s steps.proxy_check=4s steps.email_wait=5s steps.browser_shutdown_wait=1s`,
		},
		{
			name: "cancelled attempt",
			rec:  metered(2*time.Second, 1, step{"proxy_check", phaseBrowser, time.Second}, step{"navigate", phaseBrowser, time.Second}),
			err:  fmt.Errorf("navigating to registration page: %w", context.Canceled),
			want: `level=ERROR msg="account creation failed" outcome=failure category=cancelled ` +
				`error="navigating to registration page: context canceled" failed_step=navigate failed_phase=browser ` + counts +
				` duration=2s steps.proxy_check=1s steps.navigate=1s`,
		},
		{
			name:    "account timeout in the challenge wait",
			rec:     metered(time.Minute, 1, step{"proxy_check", phaseBrowser, time.Second}, step{"challenge", phaseBrowser, 59 * time.Second}),
			err:     fmt.Errorf("waiting for clearance: %w", browser.ErrChallengeTimeout),
			expired: true,
			want: `level=ERROR msg="account creation failed" outcome=failure category=account_timeout ` +
				`error="waiting for clearance: cloudflare challenge timed out" failed_step=challenge failed_phase=browser ` + counts +
				` duration=1m0s steps.proxy_check=1s steps.challenge=59s`,
		},
		{
			name: "save failure",
			rec:  metered(time.Second, -1, step{"proxy_check", phaseBrowser, time.Second}),
			acct: &account.Account{Email: "user@example.com", Username: "user", Password: "pw"},
			err:  fmt.Errorf("%w: writing account line: disk full", errSaveFailed),
			want: `level=ERROR msg="account creation failed" outcome=failure category=save_failed ` +
				`error="account not saved: writing account line: disk full" email=user@example.com username=user ` + counts +
				` duration=1s steps.proxy_check=1s`,
		},
		{
			name: "unconfirmed failure",
			rec:  metered(3*time.Second, 1, step{"proxy_check", phaseBrowser, time.Second}, step{"wait_account_creation", phaseHTTP, 2 * time.Second}),
			acct: &account.Account{Email: "user@example.com", Username: "user", Password: "pw", Unconfirmed: true},
			err:  fmt.Errorf("polling the registration state: %w", context.Canceled),
			want: `level=ERROR msg="account creation failed" outcome=failure category=cancelled ` +
				`error="polling the registration state: context canceled" email=user@example.com username=user unconfirmed=true ` +
				`failed_step=wait_account_creation failed_phase=http ` + counts + ` duration=3s steps.proxy_check=1s steps.wait_account_creation=2s`,
		},
		{
			name: "unconfirmed 2FA activation",
			rec:  metered(time.Second, -1, step{"proxy_check", phaseBrowser, time.Second}),
			acct: &account.Account{Email: "user@example.com", Username: "user", TOTPSecret: "S", Unconfirmed: true},
			want: `level=INFO msg="account created" outcome=success email=user@example.com username=user unconfirmed=true 2fa=true ` + counts +
				` duration=1s steps.proxy_check=1s`,
		},
		{
			name: "direct connection",
			rec:  &attemptRecord{total: time.Second, steps: &stepTracker{steps: []step{{"proxy_check", phaseBrowser, time.Second}}, failed: -1}},
			acct: &account.Account{Email: "user@example.com", Username: "user"},
			want: `level=INFO msg="account created" outcome=success email=user@example.com username=user 2fa=false proxy="" duration=1s steps.proxy_check=1s`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			if tt.expired {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			}
			var buf bytes.Buffer
			res := newAttemptResult(ctx, tt.rec, tt.acct, tt.err)
			logAttempt(ctx, logtest.New(&buf), &res)
			if got := buf.String(); got != tt.want+"\n" {
				t.Errorf("record line =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}
