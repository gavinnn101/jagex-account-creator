package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"jagex_account_creator/internal/account"
	"jagex_account_creator/internal/browser"
	"jagex_account_creator/internal/logtest"
)

// zeroTime drops the time of each record, so that neither handler writes one
// and a test can compare whole lines.
type zeroTime struct {
	slog.Handler

	t *testing.T
}

func (h zeroTime) Handle(ctx context.Context, r slog.Record) error {
	r.Time = time.Time{}
	if err := h.Handler.Handle(ctx, r); err != nil {
		h.t.Fatalf("Handle: %v", err)
	}
	return nil
}

// testLogger returns the logger of newLogger on buf, with no record time.
func testLogger(t *testing.T, buf *bytes.Buffer, terminal, noColor bool, level slog.Leveler) *slog.Logger {
	t.Helper()
	return slog.New(zeroTime{newLogger(buf, terminal, noColor, level).Handler(), t})
}

// recordOf returns a metered record with the byte counts of a typical attempt.
func recordOf(total time.Duration, failed int, steps ...step) *attemptRecord {
	return &attemptRecord{
		proxy: "203.0.113.1:8080", metered: true,
		browserBytesSent: 61234, browserBytesReceived: 543210, httpBytesSent: 23456, httpBytesReceived: 65432,
		total: total, steps: &stepTracker{steps: steps, failed: failed},
	}
}

func TestNewLoggerRecordLine(t *testing.T) {
	const terminalCounts = "proxy=203.0.113.1:8080 browser_bytes_sent=59.8KiB browser_bytes_received=530.5KiB " +
		"http_bytes_sent=22.9KiB http_bytes_received=63.9KiB"
	success := func() *attemptRecord {
		return recordOf(17361*time.Millisecond, -1, step{"proxy_check", phaseBrowser, 412 * time.Millisecond}, step{"submit_password", phaseHTTP, 377 * time.Millisecond})
	}
	failure := func() *attemptRecord {
		return recordOf(31520*time.Millisecond, 1, step{"proxy_check", phaseBrowser, 412 * time.Millisecond}, step{"challenge", phaseBrowser, 3311 * time.Millisecond})
	}
	clearanceErr := fmt.Errorf("waiting for clearance: %w", browser.ErrChallengeTimeout)
	tests := []struct {
		name    string
		noColor bool
		rec     *attemptRecord
		acct    *account.Account
		err     error
		want    string
	}{
		{
			name:    "success",
			noColor: true,
			rec:     success(),
			acct:    &account.Account{Email: "user@example.com", Username: "user", TOTPSecret: "S"},
			want:    "INF account created outcome=success email=user@example.com username=user 2fa=true " + terminalCounts + " duration=17.361s",
		},
		{
			name:    "failure",
			noColor: true,
			rec:     failure(),
			err:     clearanceErr,
			want: `ERR account creation failed outcome=failure category=challenge_timeout ` +
				`error="waiting for clearance: cloudflare challenge timed out" failed_step=challenge failed_phase=browser ` +
				terminalCounts + " duration=31.52s",
		},
		{
			name: "failure in color",
			rec:  failure(),
			err:  clearanceErr,
			want: "\x1b[91mERR\x1b[0m account creation failed \x1b[2moutcome=\x1b[0mfailure \x1b[2mcategory=\x1b[0mchallenge_timeout " +
				"\x1b[2;91merror=\x1b[22m\"waiting for clearance: cloudflare challenge timed out\"\x1b[0m " +
				"\x1b[2mfailed_step=\x1b[0mchallenge \x1b[2mfailed_phase=\x1b[0mbrowser \x1b[2mproxy=\x1b[0m203.0.113.1:8080 " +
				"\x1b[2mbrowser_bytes_sent=\x1b[0m59.8KiB \x1b[2mbrowser_bytes_received=\x1b[0m530.5KiB " +
				"\x1b[2mhttp_bytes_sent=\x1b[0m22.9KiB \x1b[2mhttp_bytes_received=\x1b[0m63.9KiB \x1b[2mduration=\x1b[0m31.52s",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			res := newAttemptResult(t.Context(), tt.rec, tt.acct, tt.err)
			logAttempt(t.Context(), testLogger(t, &buf, true, tt.noColor, slog.LevelInfo), &res)
			if got := buf.String(); got != tt.want+"\n" {
				t.Errorf("record line =\n%q\nwant\n%q", got, tt.want+"\n")
			}
		})
	}
}

func TestNewLoggerTerminalAttrScope(t *testing.T) {
	var buf bytes.Buffer
	testLogger(t, &buf, true, true, slog.LevelInfo).Info("upstream",
		slog.String("http_bytes_sent", "none"),
		slog.Group("stats", slog.Int64("upstream_bytes_sent", 2048), slog.String("error", "refused")))
	const want = "INF upstream http_bytes_sent=none stats.upstream_bytes_sent=2048 stats.error=refused\n"
	if got := buf.String(); got != want {
		t.Errorf("line =\n%q\nwant\n%q", got, want)
	}
}

func TestNewLoggerFileLineMatchesTextHandler(t *testing.T) {
	rec := recordOf(31520*time.Millisecond, 1, step{"proxy_check", phaseBrowser, 412 * time.Millisecond}, step{"challenge", phaseBrowser, 3311 * time.Millisecond})
	err := fmt.Errorf("waiting for clearance: %w", browser.ErrChallengeTimeout)

	var got, want bytes.Buffer
	res := newAttemptResult(t.Context(), rec, nil, err)
	logAttempt(t.Context(), testLogger(t, &got, false, true, slog.LevelDebug), &res)
	logAttempt(t.Context(), logtest.New(&want), &res)
	if got.String() != want.String() {
		t.Errorf("file line =\n%s\nwant\n%s", got.String(), want.String())
	}
}

func TestNewLoggerLevel(t *testing.T) {
	for _, terminal := range []bool{true, false} {
		t.Run(fmt.Sprintf("terminal=%t", terminal), func(t *testing.T) {
			var level slog.LevelVar
			var buf bytes.Buffer
			logger := testLogger(t, &buf, terminal, true, &level)
			logger.Debug("hidden")
			level.Set(slog.LevelDebug)
			logger.Debug("shown")
			if got := buf.String(); !bytes.Contains(buf.Bytes(), []byte("shown")) || bytes.Contains(buf.Bytes(), []byte("hidden")) {
				t.Errorf("output = %q, want the line logged after the level change only", got)
			}
		})
	}
}
