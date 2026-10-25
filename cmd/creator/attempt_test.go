package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/mafredri/cdp"
	"github.com/mafredri/cdp/protocol/page"

	"jagex_account_creator/internal/account"
	"jagex_account_creator/internal/api"
	"jagex_account_creator/internal/browser"
	"jagex_account_creator/internal/browser/cdptest"
	"jagex_account_creator/internal/email"
	"jagex_account_creator/internal/jagexapi"
	"jagex_account_creator/internal/logtest"
	"jagex_account_creator/internal/proxy"
	"jagex_account_creator/internal/store"
)

func TestAwaitShutdown(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantFailed string
	}{
		{name: "success"},
		{name: "an HTTP error fails the open step", err: errors.New("submit failed"), wantFailed: "submit_password"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				a := &attempt{steps: newStepTracker(time.Now(), "submit_password", phaseHTTP)}
				time.Sleep(time.Second)
				done := make(chan struct{})
				go func() {
					time.Sleep(2 * time.Second)
					close(done)
				}()

				a.awaitShutdown(tt.err, done)
				total := a.steps.finish()

				want := []step{{"submit_password", phaseHTTP, time.Second}, {"browser_shutdown_wait", phaseBrowser, 2 * time.Second}}
				if !slices.Equal(a.steps.steps, want) || total != 3*time.Second {
					t.Errorf("steps = %v, total %v, want %v, 3s", a.steps.steps, total, want)
				}
				failed, _ := a.steps.failedStep()
				if failed.name != tt.wantFailed {
					t.Errorf("failed step = %q, want %q", failed.name, tt.wantFailed)
				}
			})
		})
	}
}

func TestLoadRegistrationPage(t *testing.T) {
	const tunnelFailed = "net::ERR_TUNNEL_CONNECTION_FAILED"
	// <count> stands for the number of URL patterns that the block sent.
	const blocked = `level=DEBUG msg="blocked URL patterns set" count=<count>` + "\n" +
		`level=DEBUG msg="navigating to registration page"` + "\n"
	regURL := jagexapi.ManageRegistrationURL
	tunnelErr := "navigation error for " + regURL + ": " + tunnelFailed + ": " + browser.ErrProxyConnection.Error()
	tests := []struct {
		name        string
		navError    string // the net error of the navigation, or ""
		noLoad      bool   // whether LoadEventFired never fires
		clearErr    error
		blockErr    error
		wantErr     string
		wantCooled  bool
		wantCalls   []string // the Network calls, in call order
		wantNavs    int
		wantElapsed time.Duration
		wantLog     string
	}{
		{
			name:       "a tunnel failure cools the proxy",
			navError:   tunnelFailed,
			wantErr:    tunnelErr,
			wantCooled: true,
			wantCalls:  []string{"ClearBrowserCookies", "SetBlockedURLs"},
			wantNavs:   1,
			wantLog: blocked + `level=WARN msg="proxy in cooldown" reason="page load" proxy=` + testProxyAddr +
				` error=` + strconv.Quote(tunnelErr) + ` cooldown=5m0s failures=1` + "\n",
		},
		{
			name:        "a load timeout is no error",
			noLoad:      true,
			wantCalls:   []string{"ClearBrowserCookies", "SetBlockedURLs"},
			wantNavs:    1,
			wantElapsed: 30 * time.Second,
			wantLog:     blocked + fmt.Sprintf(`level=WARN msg="page load timed out, continuing anyway" url=%q`, regURL) + "\n",
		},
		{
			name:      "a clear error stops before the block",
			clearErr:  errors.New("clear refused"),
			wantErr:   "clearing browser cookies: clear refused",
			wantCalls: []string{"ClearBrowserCookies"},
		},
		{
			name:      "a block error stops before the navigate",
			blockErr:  errors.New("block refused"),
			wantErr:   "setting blocked URLs: block refused",
			wantCalls: []string{"ClearBrowserCookies", "SetBlockedURLs"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				loads := []*page.LoadEventFiredReply{{}}
				if tt.noLoad {
					loads = nil
				}
				p := &cdptest.Page{DOMContent: cdptest.NewStream(&page.DOMContentEventFiredReply{}), Load: cdptest.NewStream(loads...)}
				if tt.navError != "" {
					p.Navigation.ErrorText = &tt.navError
				}
				n := &cdptest.Network{ClearBrowserCookiesErr: tt.clearErr, SetBlockedURLsErr: tt.blockErr}
				var buf bytes.Buffer
				a := &attempt{
					run:    &runner{proxyMgr: proxyManager(t, testProxyAddr)},
					p:      testProxy(),
					logger: logtest.New(&buf),
					steps:  newStepTracker(time.Now(), "cdp_connect", phaseBrowser),
				}

				err := a.loadRegistrationPage(t.Context(), &cdp.Client{Page: p, Network: n})
				a.steps.finish()

				gotErr := ""
				if err != nil {
					gotErr = err.Error()
				}
				if gotErr != tt.wantErr {
					t.Errorf("error = %v, want %q", err, tt.wantErr)
				}
				if got := errors.Is(err, errProxyCooled) && errors.Is(err, browser.ErrProxyConnection); got != tt.wantCooled {
					t.Errorf("error marked as a cooled proxy connection failure = %v, want %v", got, tt.wantCooled)
				}
				// The error rows show the order: a failed call stops every call after it.
				if !slices.Equal(n.Calls, tt.wantCalls) || len(p.URLs) != tt.wantNavs {
					t.Errorf("network calls = %q, navigations = %q, want %q and %d navigations", n.Calls, p.URLs, tt.wantCalls, tt.wantNavs)
				}
				wantSteps := []step{{"cdp_connect", phaseBrowser, 0}, {"navigate", phaseBrowser, tt.wantElapsed}}
				if !slices.Equal(a.steps.steps, wantSteps) {
					t.Errorf("steps = %v, want %v", a.steps.steps, wantSteps)
				}
				wantLog := tt.wantLog
				if len(n.BlockedURLs) == 1 {
					wantLog = strings.ReplaceAll(wantLog, "<count>", strconv.Itoa(len(n.BlockedURLs[0])))
				}
				if got := buf.String(); got != wantLog {
					t.Errorf("log =\n%s\nwant\n%s", got, wantLog)
				}
			})
		})
	}
}

// finishCase is one attempt that finish ends.
type finishCase struct {
	name       string
	run        func(a *attempt) error
	acct       *account.Account // the account of a failed attempt
	cancelled  bool             // the attempt context has ended
	closed     bool             // the store is closed before the attempt
	proxied    bool             // the attempt runs through testProxy() of its own manager
	cooled     bool             // an earlier failure put the proxy in cooldown at the attempt start. Needs proxied
	wantErr    string
	wantLine   string        // every log line. <error> stands for wantErr as slog quotes it
	wantExport string        // the export of the store after the attempt. A closed store has none
	wantWait   time.Duration // the wait of the manager's Next after the attempt
}

// historyWarn is the WARN line of an attempt row that a closed store refuses.
const historyWarn = `level=WARN msg="attempt not saved to the run history" run_id=1 worker=0 error="saving attempt: sql: database is closed"`

// testFinish runs each case on the fake clock with no forward proxy, so the lines are exact.
// A closed store also refuses the attempt row, so its case gets historyWarn once after the record line.
func testFinish(t *testing.T, tests []finishCase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := newTestStore(t)
				history := newTestHistory(t, s)
				if tt.closed {
					_ = s.Close()
				}
				var buf bytes.Buffer
				state := testRunState(1)
				state.start(1)
				start := time.Now()
				a := &attempt{run: &runner{history: history}, logger: logtest.New(&buf), worker: workerState{state: state}, steps: newStepTracker(start, "proxy_check", phaseBrowser)}
				if tt.proxied {
					a.run.proxyMgr, a.p = proxyManager(t, testProxyAddr), testProxy()
				}
				if tt.cooled {
					a.run.proxyMgr.ReportFailure(a.p)
				}
				a.worker.attemptStarted(a.p, time.Now())

				runErr := tt.run(a)
				acct := tt.acct
				if runErr == nil {
					acct = &account.Account{Email: "user@example.com", Username: "user", Password: "pw"}
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if tt.cancelled {
					cancel()
				}
				gotErr := ""
				finishErr := a.finish(ctx, acct, runErr, nil, nil)
				if finishErr != nil {
					gotErr = finishErr.Error()
				}

				wantLine := strings.ReplaceAll(tt.wantLine, "<error>", strconv.Quote(tt.wantErr))
				if gotErr != tt.wantErr {
					t.Errorf("error = %q, want %q", gotErr, tt.wantErr)
				}
				// Only a cooled proxy marks the error, so a direct connection backs off.
				if got, want := errors.Is(finishErr, errProxyCooled), tt.wantWait > 0; got != want {
					t.Errorf("error marked as cooled = %v, want %v", got, want)
				}
				// The written mark ends the account number of a failed attempt, so only an account in the store sets it.
				if got, want := errors.Is(finishErr, errAccountWritten), finishErr != nil && tt.wantExport != ""; got != want {
					t.Errorf("error marked as written = %v, want %v", got, want)
				}
				wantLog := wantLine + "\n"
				if tt.closed {
					wantLog += historyWarn + "\n"
				}
				if got := buf.String(); got != wantLog {
					t.Errorf("log =\n%s\nwant\n%s", got, wantLog)
				}
				if !tt.closed {
					if got := readRecords(t, s); got != tt.wantExport {
						t.Errorf("export = %q, want %q", got, tt.wantExport)
					}
					assertAttemptRow(t, s, start, tt, wantLine)
				}
				assertFinishState(t, state.snapshot(), tt, wantLine)
				if tt.proxied {
					if got := nextWait(t, a.run.proxyMgr); got != tt.wantWait {
						t.Errorf("Next waited %v, want exactly %v", got, tt.wantWait)
					}
				}
			})
		})
	}
}

var (
	categoryPattern = regexp.MustCompile(`category=(\w+)`)
	cooldownPattern = regexp.MustCompile(`msg="proxy in cooldown" reason="([^"]+)"`)
	failedPattern   = regexp.MustCompile(` failed_step=(\w+) failed_phase=(\w+)`)
	proxyPattern    = regexp.MustCompile(` proxy=(\S+)`)
	durationPattern = regexp.MustCompile(` duration=(\S+)`)
	stepPattern     = regexp.MustCompile(` steps\.(\w+)=(\S+)`)
)

// parseDuration returns the duration text of a record line.
func parseDuration(t *testing.T, text string) time.Duration {
	t.Helper()
	d, err := time.ParseDuration(text)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// wantAttemptRow returns the attempt row that the record line of wantLine
// describes. The record line is the last line. The account of the row is the account of the export.
func wantAttemptRow(t *testing.T, start time.Time, tt finishCase, wantLine string) store.Attempt {
	t.Helper()
	line := wantLine[strings.LastIndex(wantLine, "\n")+1:]
	want := store.Attempt{ID: 1, RunID: 1, StartedAt: start.UTC(), Error: tt.wantErr, Steps: []store.Step{}}
	if m := categoryPattern.FindStringSubmatch(line); m != nil {
		want.Category = m[1]
	}
	if m := failedPattern.FindStringSubmatch(line); m != nil {
		want.FailedStep, want.FailedPhase = m[1], m[2]
	}
	if m := proxyPattern.FindStringSubmatch(line); m[1] != `""` {
		want.Proxy = m[1]
	}
	want.Duration = parseDuration(t, durationPattern.FindStringSubmatch(line)[1])
	for _, m := range stepPattern.FindAllStringSubmatch(line, -1) {
		want.Steps = append(want.Steps, store.Step{Name: m[1], Duration: parseDuration(t, m[2])})
	}
	if tt.wantExport != "" {
		want.AccountID, want.Email, want.Username = 1, "user@example.com", "user"
	}
	return want
}

// assertAttemptRow checks that the one attempt row of s holds the values of the record line of wantLine.
func assertAttemptRow(t *testing.T, s *store.Store, start time.Time, tt finishCase, wantLine string) {
	t.Helper()
	rows, _, err := s.Attempts(t.Context(), 1, 0, 10)
	if err != nil {
		t.Fatalf("Attempts: %v", err)
	}
	if g, w := jsonOf(t, rows), jsonOf(t, []store.Attempt{wantAttemptRow(t, start, tt, wantLine)}); g != w {
		t.Errorf("attempt rows =\n%s\nwant\n%s", g, w)
	}
}

// assertFinishState checks the run state after the one attempt of tt: the
// worker is idle, and the counts and the cooldowns follow the record line and
// the export. A cooldown WARN in the line is a cooldown that ends wantWait from now.
func assertFinishState(t *testing.T, st api.RunState, tt finishCase, wantLine string) {
	t.Helper()
	want := api.RunCounts{Attempts: 1, Failures: map[string]int{}}
	if m := categoryPattern.FindStringSubmatch(wantLine); m != nil {
		want.Failures[m[1]] = 1
	}
	if tt.wantExport != "" {
		want.Accounts = 1
	}
	if strings.Contains(tt.wantExport, `"unconfirmed":true`) {
		want.Unconfirmed = 1
	}
	wantCooldowns := []api.RunCooldown{}
	if m := cooldownPattern.FindStringSubmatch(wantLine); m != nil {
		wantCooldowns = append(wantCooldowns, api.RunCooldown{Proxy: testProxyAddr, Reason: m[1], Until: time.Now().Add(tt.wantWait), Failures: 1})
	}
	got := api.RunState{Workers: st.Workers, Cooldowns: st.Cooldowns, Counts: st.Counts}
	wantState := api.RunState{Workers: []api.RunWorker{{Worker: 0}}, Cooldowns: wantCooldowns, Counts: want}
	if g, w := jsonOf(t, got), jsonOf(t, wantState); g != w {
		t.Errorf("run state =\n%s\nwant\n%s", g, w)
	}
}

func TestNewAttempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		state := testRunState(1)
		state.start(1)
		ws := workerState{state: state}
		waitStart := time.Now()
		ws.attemptStarted(testProxy(), waitStart)
		time.Sleep(time.Second)
		released := false
		slot := browserSlot{waitStart: waitStart, release: func() { released = true }}
		provider := &email.IMAPProvider{}
		r := &runner{}
		var buf bytes.Buffer

		a := r.newAttempt(logtest.New(&buf), ws, provider, testProxy(), slot)
		time.Sleep(2 * time.Second)
		a.steps.begin("navigate", phaseBrowser)
		a.steps.finish()
		a.releaseSlot()

		if a.run != r || a.worker != ws || a.emailProvider != provider || a.p != testProxy() || !released {
			t.Errorf("attempt = %+v, released %v, want the runner, the handle, the provider, the proxy and the slot release", a, released)
		}
		wantSteps := []step{{stepSlotWait, phaseBrowser, time.Second}, {"proxy_check", phaseBrowser, 2 * time.Second}, {"navigate", phaseBrowser, 0}}
		if !slices.Equal(a.steps.steps, wantSteps) {
			t.Errorf("steps = %v, want %v", a.steps.steps, wantSteps)
		}
		wantRow := api.RunWorker{Attempt: &api.RunAttempt{StartedAt: waitStart, Proxy: new(testProxyAddr), Step: "navigate", StepPhase: phaseBrowser, StepStartedAt: waitStart.Add(3 * time.Second)}}
		if got, want := jsonOf(t, state.snapshot().Workers), jsonOf(t, []api.RunWorker{wantRow}); got != want {
			t.Errorf("worker rows = %s, want %s", got, want)
		}
		if want := `level=INFO msg="starting account creation" proxy=` + testProxyAddr + "\n"; buf.String() != want {
			t.Errorf("log = %q, want %q", buf.String(), want)
		}
	})
}

func TestFinish(t *testing.T) {
	const acctJSON = `{"email":"user@example.com","username":"user","password":"pw","created_at":"0001-01-01T00:00:00Z"}`
	const rateErr = "initial registration: rate limited: status 429"
	const rateLine = `level=ERROR msg="account creation failed" outcome=failure category=rate_limited error="` + rateErr + `" ` +
		`failed_step=submit_registration failed_phase=http proxy=` + testProxyAddr + ` duration=3s steps.proxy_check=1s steps.submit_registration=2s`
	// rateLimited runs the proxy check for 1s and the registration for 2s, then gets a 429.
	rateLimited := func(a *attempt) error {
		time.Sleep(time.Second)
		a.steps.begin("submit_registration", phaseHTTP)
		time.Sleep(2 * time.Second)
		return fmt.Errorf("initial registration: %w: status 429", jagexapi.ErrRateLimited)
	}
	testFinish(t, []finishCase{
		{
			name: "success writes the account",
			run: func(a *attempt) error {
				time.Sleep(time.Second)
				a.steps.begin("submit_password", phaseHTTP)
				time.Sleep(2 * time.Second)
				return nil
			},
			wantLine: `level=INFO msg="account created" outcome=success email=user@example.com username=user 2fa=false proxy="" ` +
				`duration=3s steps.proxy_check=1s steps.submit_password=2s`,
			wantExport: acctJSON + "\n",
		},
		{
			name: "browser phase failure",
			run: func(a *attempt) error {
				time.Sleep(time.Second)
				a.steps.begin("challenge", phaseBrowser)
				time.Sleep(30 * time.Second)
				return fmt.Errorf("waiting for clearance: %w", browser.ErrChallengeTimeout)
			},
			wantErr: "waiting for clearance: cloudflare challenge timed out",
			wantLine: `level=ERROR msg="account creation failed" outcome=failure category=challenge_timeout ` +
				`error="waiting for clearance: cloudflare challenge timed out" failed_step=challenge failed_phase=browser proxy="" ` +
				`duration=31s steps.proxy_check=1s steps.challenge=30s`,
		},
		{
			name: "http phase failure",
			run: func(a *attempt) error {
				time.Sleep(time.Second)
				a.steps.begin("email_wait", phaseHTTP)
				time.Sleep(2 * time.Minute)
				err := fmt.Errorf("waiting for verification code: %w", email.ErrEmailTimeout)
				done := make(chan struct{})
				go func() {
					time.Sleep(3 * time.Second)
					close(done)
				}()
				a.awaitShutdown(err, done)
				return err
			},
			wantErr: "waiting for verification code: email verification timed out",
			wantLine: `level=ERROR msg="account creation failed" outcome=failure category=email_timeout ` +
				`error="waiting for verification code: email verification timed out" failed_step=email_wait failed_phase=http proxy="" ` +
				`duration=2m4s steps.proxy_check=1s steps.email_wait=2m0s steps.browser_shutdown_wait=3s`,
		},
		{
			name:    "a failed write fails the attempt",
			run:     func(*attempt) error { time.Sleep(time.Second); return nil },
			closed:  true,
			wantErr: "account not saved: saving account: sql: database is closed",
			wantLine: `level=ERROR msg="account creation failed" outcome=failure category=save_failed error=<error> ` +
				`email=user@example.com username=user proxy="" duration=1s steps.proxy_check=1s`,
		},
		{
			name:    "a rate limit cools the proxy",
			run:     rateLimited,
			proxied: true,
			wantErr: rateErr,
			wantLine: `level=WARN msg="proxy in cooldown" reason="rate limited" proxy=` + testProxyAddr + ` error="` + rateErr + `" cooldown=5m0s failures=1` + "\n" +
				rateLine,
			wantWait: 5 * time.Minute,
		},
		{
			name:     "a rate limit during a running cooldown keeps that cooldown",
			run:      rateLimited,
			proxied:  true,
			cooled:   true,
			wantErr:  rateErr,
			wantLine: rateLine,
			wantWait: 5*time.Minute - 3*time.Second,
		},
		{
			name: "another failure keeps the proxy",
			run: func(a *attempt) error {
				time.Sleep(time.Second)
				a.steps.begin("submit_registration", phaseHTTP)
				time.Sleep(2 * time.Second)
				return fmt.Errorf("initial registration: %w: status 403", jagexapi.ErrRegistrationFailed)
			},
			proxied: true,
			wantErr: "initial registration: account registration failed: status 403",
			wantLine: `level=ERROR msg="account creation failed" outcome=failure category=registration_failed ` +
				`error="initial registration: account registration failed: status 403" failed_step=submit_registration failed_phase=http proxy=` + testProxyAddr + ` ` +
				`duration=3s steps.proxy_check=1s steps.submit_registration=2s`,
		},
		{
			name: "a rate limit on a direct connection cools nothing",
			run: func(*attempt) error {
				time.Sleep(time.Second)
				return fmt.Errorf("initial registration: %w: status 429", jagexapi.ErrRateLimited)
			},
			wantErr: rateErr,
			wantLine: `level=ERROR msg="account creation failed" outcome=failure category=rate_limited error="` + rateErr + `" ` +
				`failed_step=proxy_check failed_phase=browser proxy="" duration=1s steps.proxy_check=1s`,
		},
	})
}

// The account of each case is past the password POST, so finish writes it also after an error.
func TestFinishUnconfirmed(t *testing.T) {
	const unconfirmedJSON = `{"email":"user@example.com","username":"user","password":"pw","created_at":"0001-01-01T00:00:00Z","unconfirmed":true}`
	const waitRateErr = "reading the registration state: rate limited: status 429"
	const waitSteps = `duration=4s steps.proxy_check=1s steps.submit_password=1s steps.wait_account_creation=2s`
	// inWait sends the password for 1s and waits for the account for 2s. Then it fails with err.
	inWait := func(err error) func(a *attempt) error {
		return func(a *attempt) error {
			time.Sleep(time.Second)
			a.steps.begin("submit_password", phaseHTTP)
			time.Sleep(time.Second)
			a.steps.begin("wait_account_creation", phaseHTTP)
			time.Sleep(2 * time.Second)
			return err
		}
	}
	unconfirmed := func() *account.Account {
		return &account.Account{Email: "user@example.com", Username: "user", Password: "pw", Unconfirmed: true}
	}
	testFinish(t, []finishCase{
		{
			name:      "a cancel during the wait writes the unconfirmed account",
			run:       inWait(fmt.Errorf("polling the registration state: %w", context.Canceled)),
			acct:      unconfirmed(),
			cancelled: true,
			wantErr:   "polling the registration state: context canceled",
			wantLine: `level=ERROR msg="account creation failed" outcome=failure category=cancelled error="polling the registration state: context canceled" ` +
				`email=user@example.com username=user unconfirmed=true failed_step=wait_account_creation failed_phase=http proxy="" ` + waitSteps,
			wantExport: unconfirmedJSON + "\n",
		},
		{
			name:      "a failed write of an unconfirmed account keeps both errors",
			run:       inWait(fmt.Errorf("polling the registration state: %w", context.Canceled)),
			acct:      unconfirmed(),
			cancelled: true,
			closed:    true,
			wantErr:   "account not saved: saving account: sql: database is closed: polling the registration state: context canceled",
			wantLine: `level=ERROR msg="account creation failed" outcome=failure category=save_failed error=<error> ` +
				`email=user@example.com username=user unconfirmed=true failed_step=wait_account_creation failed_phase=http proxy="" ` + waitSteps,
		},
		{
			name:    "a rate limit during the wait cools the proxy and writes the account",
			run:     inWait(fmt.Errorf("reading the registration state: %w: status 429", jagexapi.ErrRateLimited)),
			acct:    unconfirmed(),
			proxied: true,
			wantErr: waitRateErr,
			wantLine: `level=WARN msg="proxy in cooldown" reason="rate limited" proxy=` + testProxyAddr + ` error="` + waitRateErr + `" cooldown=5m0s failures=1` + "\n" +
				`level=ERROR msg="account creation failed" outcome=failure category=rate_limited error="` + waitRateErr + `" ` +
				`email=user@example.com username=user unconfirmed=true failed_step=wait_account_creation failed_phase=http proxy=` + testProxyAddr + ` ` + waitSteps,
			wantExport: unconfirmedJSON + "\n",
			wantWait:   5 * time.Minute,
		},
		{
			name:      "a tail that ran out its own bound is unknown",
			run:       inWait(fmt.Errorf("polling the registration state: %w", context.DeadlineExceeded)),
			acct:      unconfirmed(),
			cancelled: true,
			wantErr:   "polling the registration state: context deadline exceeded",
			wantLine: `level=ERROR msg="account creation failed" outcome=failure category=unknown error="polling the registration state: context deadline exceeded" ` +
				`email=user@example.com username=user unconfirmed=true failed_step=wait_account_creation failed_phase=http proxy="" ` + waitSteps,
			wantExport: unconfirmedJSON + "\n",
		},
	})
}

// A refused password POST has no account, but its work ran outside the attempt context.
func TestFinishRefusedPassword(t *testing.T) {
	const refusedErr = "submitting password: account registration failed: status 400"
	const limitedErr = "submitting password: rate limited: status 429"
	const steps = `duration=3s steps.proxy_check=1s steps.submit_password=2s`
	// inPassword sends the password for 2s, then fails with err.
	inPassword := func(err error) func(a *attempt) error {
		return func(a *attempt) error {
			time.Sleep(time.Second)
			a.steps.begin("submit_password", phaseHTTP)
			time.Sleep(2 * time.Second)
			return err
		}
	}
	testFinish(t, []finishCase{
		{
			name:      "a refusal after a cancel keeps its category and writes nothing",
			run:       inPassword(markedError{err: fmt.Errorf("submitting password: %w: status 400", jagexapi.ErrRegistrationFailed), mark: errPasswordRefused}),
			cancelled: true,
			wantErr:   refusedErr,
			wantLine: `level=ERROR msg="account creation failed" outcome=failure category=registration_failed error="` + refusedErr + `" ` +
				`failed_step=submit_password failed_phase=http proxy="" ` + steps,
		},
		{
			name:      "a 429 refusal after a cancel cools the proxy and writes nothing",
			run:       inPassword(markedError{err: fmt.Errorf("submitting password: %w: status 429", jagexapi.ErrRateLimited), mark: errPasswordRefused}),
			cancelled: true,
			proxied:   true,
			wantErr:   limitedErr,
			wantLine: `level=WARN msg="proxy in cooldown" reason="rate limited" proxy=` + testProxyAddr + ` error="` + limitedErr + `" cooldown=5m0s failures=1` + "\n" +
				`level=ERROR msg="account creation failed" outcome=failure category=rate_limited error="` + limitedErr + `" ` +
				`failed_step=submit_password failed_phase=http proxy=` + testProxyAddr + ` ` + steps,
			wantWait: 5 * time.Minute,
		},
		{
			name: "a refusal before the password after a cancel is cancelled",
			run: func(a *attempt) error {
				time.Sleep(time.Second)
				a.steps.begin("submit_registration", phaseHTTP)
				time.Sleep(2 * time.Second)
				return fmt.Errorf("initial registration: %w: status 403", jagexapi.ErrRegistrationFailed)
			},
			cancelled: true,
			wantErr:   "initial registration: account registration failed: status 403",
			wantLine: `level=ERROR msg="account creation failed" outcome=failure category=cancelled ` +
				`error="initial registration: account registration failed: status 403" failed_step=submit_registration failed_phase=http proxy="" ` +
				`duration=3s steps.proxy_check=1s steps.submit_registration=2s`,
		},
	})
}

func TestFinishStopsForwardProxies(t *testing.T) {
	p := proxy.Proxy{Host: "203.0.113.1", Port: "8080"}
	const counts = `proxy=203.0.113.1:8080 browser_bytes_sent=0 browser_bytes_received=0 http_bytes_sent=0 http_bytes_received=0 duration=`
	tests := []struct {
		name       string
		err        error
		noHTTP     bool // the HTTP forward proxy failed to start
		wantLine   string
		wantExport string
	}{
		{
			name:       "success",
			wantLine:   `level=INFO msg="account created" outcome=success email=user@example.com username=user 2fa=false ` + counts,
			wantExport: `"proxy":"203.0.113.1:8080"}` + "\n",
		},
		{
			name:     "failure before the HTTP forward proxy starts",
			err:      errors.New("starting forward proxy: listening: refused"),
			noHTTP:   true,
			wantLine: `level=ERROR msg="account creation failed" outcome=failure category=unknown error="starting forward proxy: listening: refused" failed_step=proxy_check failed_phase=browser ` + counts,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			var buf bytes.Buffer
			a := &attempt{run: &runner{history: newTestHistory(t, s)}, logger: logtest.New(&buf), p: p, steps: newStepTracker(time.Now(), "proxy_check", phaseBrowser)}
			fwds := make([]*proxy.ForwardProxy, 2)
			for i := range fwds {
				if i == 1 && tt.noHTTP {
					break
				}
				fwd, _, err := startForwardProxy(t.Context(), slog.New(slog.DiscardHandler), p)
				if err != nil {
					t.Fatalf("startForwardProxy: %v", err)
				}
				fwds[i] = fwd
			}
			var acct *account.Account
			if tt.err == nil {
				acct = &account.Account{Email: "user@example.com", Username: "user", Proxy: p.HostPort()}
			}

			_ = a.finish(t.Context(), acct, tt.err, fwds[0], fwds[1])

			if !strings.HasPrefix(buf.String(), tt.wantLine) {
				t.Errorf("record line =\n%s\nwant the prefix\n%s", buf.String(), tt.wantLine)
			}
			for _, fwd := range fwds {
				if fwd == nil {
					continue
				}
				if conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", fwd.Addr()); err == nil {
					_ = conn.Close()
					t.Errorf("forward proxy %s still listens after finish", fwd.Addr())
				}
			}
			if got := readRecords(t, s); !strings.HasSuffix(got, tt.wantExport) || strings.Contains(got, "bytes") {
				t.Errorf("export = %q, want the suffix %q and no byte keys", got, tt.wantExport)
			}
		})
	}
}
