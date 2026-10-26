package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sardanioss/httpcloak/client"

	"jagex_account_creator/internal/account"
	"jagex_account_creator/internal/config"
	"jagex_account_creator/internal/jagexapi"
	"jagex_account_creator/internal/logtest"
	"jagex_account_creator/internal/useragent"
)

func TestNewHTTPClient(t *testing.T) {
	hints := useragent.ClientHints{SecChUA: `"Chromium";v="145"`, SecChUAMobile: "?0", SecChUAPlatform: `"Linux"`}
	session := browserSession{
		cookies:   []client.Cookie{{Name: "cf_clearance", Value: "v", Domain: ".jagex.com", Path: "/"}},
		userAgent: "test-agent",
		hints:     hints,
	}

	tests := []struct {
		name      string
		httpProxy string
		wantProxy string // the "proxy set" log line, or "" when none is logged
	}{
		{name: "direct connection"},
		{
			name:      "forward proxy",
			httpProxy: "http://127.0.0.1:8080",
			wantProxy: `msg="proxy set" component=http-client proxy=http://127.0.0.1:8080`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := logtest.New(&buf)
			a := &attempt{logger: logger, steps: newStepTracker(time.Now(), "navigate", phaseBrowser)}

			httpClient := a.newHTTPClient(session, tt.httpProxy)
			defer httpClient.Close()

			if got := stepNames(a.steps); got != "navigate" {
				t.Errorf("steps = %q, want navigate: the builder opens no step", got)
			}
			for _, want := range []string{
				`msg="browser user agent" ua=test-agent`,
				`msg="client hints set" component=http-client sec_ch_ua="\"Chromium\";v=\"145\"" sec_ch_ua_mobile=?0 sec_ch_ua_platform="\"Linux\""`,
			} {
				if !strings.Contains(buf.String(), want) {
					t.Errorf("log has no %s:\n%s", want, buf.String())
				}
			}
			if got := strings.Contains(buf.String(), `msg="proxy set"`); got != (tt.wantProxy != "") || !strings.Contains(buf.String(), tt.wantProxy) {
				t.Errorf("log has proxy set = %v, want %q:\n%s", got, tt.wantProxy, buf.String())
			}
			if got := strings.Count(buf.String(), `msg="importing cookie" component=http-client name=cf_clearance `); got != 1 {
				t.Errorf("cf_clearance imports = %d, want 1:\n%s", got, buf.String())
			}
			if got := strings.Count(buf.String(), `msg="importing cookie"`); got != 1 {
				t.Errorf("cookie imports = %d, want 1:\n%s", got, buf.String())
			}
		})
	}
}

// isDone reports whether the fetch has returned.
func isDone(f *loginLinkFetch) bool {
	select {
	case <-f.done:
		return true
	default:
		return false
	}
}

func TestLoginLinkFetch(t *testing.T) {
	link := &url.URL{Scheme: "https", Host: "account.jagex.com", Path: "/oauth2/auth"}

	t.Run("wait returns the result each time", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := startLoginLinkFetch(t.Context(), func(context.Context) (*url.URL, error) {
				time.Sleep(2 * time.Second)
				return link, nil
			})
			defer f.stop()

			for range 2 {
				if got, err := f.wait(); got != link || err != nil {
					t.Errorf("wait = %v, %v, want %v, nil", got, err, link)
				}
			}
			if f.took != 2*time.Second {
				t.Errorf("took = %v, want 2s", f.took)
			}
		})
	})

	t.Run("stop cancels the fetch and waits for it", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := startLoginLinkFetch(t.Context(), func(ctx context.Context) (*url.URL, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			})
			synctest.Wait()
			if isDone(f) {
				t.Fatal("the fetch returned before stop")
			}

			f.stop()

			if !isDone(f) {
				t.Fatal("stop returned before the fetch")
			}
			if _, err := f.wait(); !errors.Is(err, context.Canceled) {
				t.Errorf("wait error = %v, want context.Canceled", err)
			}
		})
	})

	t.Run("the fetch outlives a gate timeout", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			// The gate makes its own timeout context, and the fetch never derives from it.
			gateCtx, cancelGate := context.WithTimeout(t.Context(), time.Second)
			defer cancelGate()
			f := startLoginLinkFetch(t.Context(), func(ctx context.Context) (*url.URL, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			})

			<-gateCtx.Done()
			time.Sleep(time.Second)
			synctest.Wait()
			if isDone(f) {
				t.Fatal("the gate timeout ended the fetch")
			}
			f.stop()
			if !isDone(f) {
				t.Fatal("stop did not end the fetch")
			}
		})
	})

	t.Run("a cancel of the attempt context ends the fetch", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			f := startLoginLinkFetch(ctx, func(ctx context.Context) (*url.URL, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			})
			defer f.stop()

			cancel()

			if _, err := f.wait(); !errors.Is(err, context.Canceled) {
				t.Errorf("wait error = %v, want context.Canceled", err)
			}
		})
	})
}

func TestRegisterOverHTTPFetchFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var buf bytes.Buffer
		logger := logtest.New(&buf)
		a := &attempt{logger: logger, steps: newStepTracker(time.Now(), "read_session", phaseBrowser)}
		httpClient := jagexapi.NewClient(logger, "test-agent", useragent.ClientHints{}, "")
		defer httpClient.Close()
		fetchErr := fmt.Errorf("getting the login link: %w", jagexapi.ErrRegistrationFailed)
		f := startLoginLinkFetch(t.Context(), func(context.Context) (*url.URL, error) {
			time.Sleep(3 * time.Second)
			return nil, fetchErr
		})
		defer f.stop()
		time.Sleep(time.Second)

		acct, err := a.registerOverHTTP(t.Context(), httpClient, f, []client.Cookie{{Name: "cf_clearance", Value: "v", Domain: ".jagex.com", Path: "/"}})
		total := a.steps.finish()

		if acct != nil || !errors.Is(err, fetchErr) || err.Error() != "starting registration: "+fetchErr.Error() {
			t.Fatalf("registerOverHTTP = %v, %v, want nil and the wrapped fetch error", acct, err)
		}
		if got := failureCategory(t.Context(), err); got != "registration_failed" {
			t.Errorf("category = %q, want registration_failed", got)
		}
		// oauth_start opens before the import and the join, so it holds the join wait.
		want := []step{{"read_session", phaseBrowser, time.Second}, {"oauth_start", phaseHTTP, 2 * time.Second}}
		if !slices.Equal(a.steps.steps, want) || total != 3*time.Second {
			t.Errorf("steps = %v, total %v, want %v, 3s", a.steps.steps, total, want)
		}
		if !strings.Contains(buf.String(), `msg="importing cookie" component=http-client name=cf_clearance `) {
			t.Errorf("log has no import line:\n%s", buf.String())
		}
		if strings.Contains(buf.String(), "login link fetched") {
			t.Errorf("a failed fetch logged the fetched line:\n%s", buf.String())
		}
	})
}

// fakeEmailProvider records each verification wait and returns code and err.
type fakeEmailProvider struct {
	code  string
	err   error
	waits []emailWait
}

// emailWait holds the arguments of one WaitForVerificationCode call.
type emailWait struct {
	address string
	since   time.Time
	timeout time.Duration
}

func (f *fakeEmailProvider) WaitForVerificationCode(_ context.Context, address string, since time.Time, timeout time.Duration) (string, error) {
	f.waits = append(f.waits, emailWait{address: address, since: since, timeout: timeout})
	return f.code, f.err
}

// fakeRegistrar records each registration call with its arguments. A call
// waits its delay under the context of the call. The call named fail returns err.
type fakeRegistrar struct {
	fail   string
	err    error
	delays map[string]time.Duration
	calls  []string
}

func (f *fakeRegistrar) call(ctx context.Context, name string, args ...string) error {
	f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
	if d := f.delays[name]; d > 0 {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	if name == f.fail {
		return f.err
	}
	return nil
}

func (f *fakeRegistrar) InitRegistration(ctx context.Context) error {
	return f.call(ctx, "InitRegistration")
}

func (f *fakeRegistrar) SubmitInitialRegistration(ctx context.Context, email, dob string) error {
	return f.call(ctx, "SubmitInitialRegistration", email, dob)
}

func (f *fakeRegistrar) SubmitVerificationCode(ctx context.Context, code string) error {
	return f.call(ctx, "SubmitVerificationCode", code)
}

func (f *fakeRegistrar) ValidateDisplayName(ctx context.Context, displayName string) error {
	return f.call(ctx, "ValidateDisplayName", displayName)
}

func (f *fakeRegistrar) SubmitPassword(ctx context.Context, displayName, password string) error {
	return f.call(ctx, "SubmitPassword", displayName, password)
}

func (f *fakeRegistrar) WaitForAccountCreation(ctx context.Context) error {
	return f.call(ctx, "WaitForAccountCreation")
}

func TestSubmitRegistration(t *testing.T) {
	id := identity{email: "user@example.com", dob: "1990-01-02", username: "player1", password: "pw-1"}
	allCalls := []string{
		"InitRegistration",
		"SubmitInitialRegistration user@example.com 1990-01-02",
		"SubmitVerificationCode 123456",
		"ValidateDisplayName player1",
	}
	allSteps := []string{"oauth_start", "init_registration", "submit_registration", "email_wait", "submit_code", "validate_name"}
	refused := errors.New("refused")

	tests := []struct {
		name      string
		fail      string // the registration call that fails, or "email" for the email wait
		cancelled bool   // the attempt context has ended
		wantCalls int    // the number of calls made, from the start of allCalls
		wantSteps int    // the number of steps opened, from the start of allSteps
	}{
		{name: "init fails", fail: "InitRegistration", wantCalls: 1, wantSteps: 2},
		{name: "submit fails", fail: "SubmitInitialRegistration", wantCalls: 2, wantSteps: 3},
		{name: "email wait fails", fail: "email", wantCalls: 2, wantSteps: 4},
		{name: "code fails", fail: "SubmitVerificationCode", wantCalls: 3, wantSteps: 5},
		{name: "name fails", fail: "ValidateDisplayName", wantCalls: 4, wantSteps: 6},
		{name: "a done context stops before the password", cancelled: true, wantCalls: 4, wantSteps: 6},
		{name: "ready for the password", wantCalls: 4, wantSteps: 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				provider := &fakeEmailProvider{code: "123456"}
				if tt.fail == "email" {
					provider.err = refused
				}
				reg := &fakeRegistrar{fail: tt.fail, err: refused}
				var buf bytes.Buffer
				a := &attempt{logger: logtest.New(&buf), emailProvider: provider, steps: newStepTracker(time.Now(), "oauth_start", phaseHTTP)}
				start := time.Now()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if tt.cancelled {
					cancel()
				}

				err := a.submitRegistration(ctx, reg, id)

				var wantErr error
				switch {
				case tt.fail != "":
					wantErr = refused
				case tt.cancelled:
					wantErr = context.Canceled
				}
				if !errors.Is(err, wantErr) || (wantErr == nil) != (err == nil) {
					t.Errorf("submitRegistration() error = %v, want %v", err, wantErr)
				}
				if want := allCalls[:tt.wantCalls]; !slices.Equal(reg.calls, want) {
					t.Errorf("calls = %q, want %q", reg.calls, want)
				}
				if got, want := stepNames(a.steps), strings.Join(allSteps[:tt.wantSteps], " "); got != want {
					t.Errorf("steps = %q, want %q", got, want)
				}
				wantWaits := 0
				if tt.wantSteps > slices.Index(allSteps, "email_wait") {
					wantWaits = 1
				}
				if len(provider.waits) != wantWaits {
					t.Fatalf("email waits = %+v, want %d", provider.waits, wantWaits)
				}
				for _, w := range provider.waits {
					if since := start.Add(-5 * time.Second); w.address != id.email || !w.since.Equal(since) || w.timeout != 2*time.Minute {
						t.Errorf("email wait = %+v, want %s since %v for 2m0s", w, id.email, since)
					}
				}
			})
		})
	}
}

func TestCreateRemoteAccount(t *testing.T) {
	id := identity{email: "user@example.com", dob: "1990-01-02", username: "player1", password: "pw-1"}
	reset := errors.New("submitting password: executing request to /states: connection reset")
	postStatus := fmt.Errorf("submitting password: %w: status 400", jagexapi.ErrRegistrationFailed)
	postLimited := fmt.Errorf("submitting password: executing request to /states: %w: status 429", jagexapi.ErrRateLimited)
	rollback := fmt.Errorf("submitting password: %w: registration state %q, reason: %s", jagexapi.ErrRegistrationFailed, "REGULAR_REGISTRATION_FINALIZING_ROLLBACK", `"TEST_REASON"`)
	forbidden := fmt.Errorf("waiting for account creation: %w: status 403", jagexapi.ErrRegistrationFailed)
	limited := fmt.Errorf("reading the registration state: %w: status 429", jagexapi.ErrRateLimited)
	const post, wait = "SubmitPassword player1 pw-1", "WaitForAccountCreation"

	tests := []struct {
		name     string
		fail     string // the call that fails
		err      error
		delays   map[string]time.Duration
		cancelAt time.Duration // the cancel of the attempt context, or 0 for none
		stopAt   time.Duration // the close of stopNow, or 0 for none
		wantErr  error
		refused  bool // Jagex refused the POST, so no account comes back
		wantTook time.Duration
	}{
		{name: "a transport error on the POST keeps the account", fail: "SubmitPassword", err: reset, wantErr: reset},
		{name: "a 4xx on the POST is a refusal", fail: "SubmitPassword", err: postStatus, wantErr: jagexapi.ErrRegistrationFailed, refused: true},
		{name: "a 429 on the POST is a refusal", fail: "SubmitPassword", err: postLimited, wantErr: jagexapi.ErrRateLimited, refused: true},
		{name: "a rollback state is a refusal", fail: "SubmitPassword", err: rollback, wantErr: jagexapi.ErrRegistrationFailed, refused: true},
		{
			name:     "the POST and the wait share the bound",
			delays:   map[string]time.Duration{"SubmitPassword": 10 * time.Second, wait: time.Hour},
			wantErr:  context.DeadlineExceeded,
			wantTook: 30 * time.Second,
		},
		{
			name:     "the tail outlives a cancel of the attempt context",
			delays:   map[string]time.Duration{wait: 3 * time.Second},
			cancelAt: time.Second,
			wantTook: 3 * time.Second,
		},
		{
			name:     "stopNow ends the tail",
			delays:   map[string]time.Duration{wait: time.Hour},
			stopAt:   5 * time.Second,
			wantErr:  context.Canceled,
			wantTook: 5 * time.Second,
		},
		{name: "a 403 in the wait", fail: wait, err: forbidden, wantErr: jagexapi.ErrRegistrationFailed},
		{name: "a 429 in the wait", fail: wait, err: limited, wantErr: jagexapi.ErrRateLimited},
		{name: "created"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				reg := &fakeRegistrar{fail: tt.fail, err: tt.err, delays: tt.delays}
				// An earlier failure whose cooldown is over shows whether the attempt reports a success.
				mgr := proxyManager(t, testProxyAddr)
				cooldown, _, _ := mgr.ReportFailure(testProxy())
				time.Sleep(cooldown)
				var buf bytes.Buffer
				a := &attempt{
					run:    &runner{proxyMgr: mgr},
					logger: logtest.New(&buf),
					p:      testProxy(),
					steps:  newStepTracker(time.Now(), "validate_name", phaseHTTP),
				}
				attemptCtx, cancelAttempt := context.WithCancel(t.Context())
				defer cancelAttempt()
				stopNow := make(chan struct{})
				if tt.cancelAt > 0 {
					time.AfterFunc(tt.cancelAt, cancelAttempt)
				}
				if tt.stopAt > 0 {
					time.AfterFunc(tt.stopAt, func() { close(stopNow) })
				}
				tail, cancel := stopNowContext(attemptCtx, stopNow)
				defer cancel()
				start := time.Now()

				acct, err := a.createRemoteAccount(tail, reg, id)

				if !errors.Is(err, tt.wantErr) {
					t.Errorf("error = %v, want %v", err, tt.wantErr)
				}
				if errors.Is(err, errPasswordRefused) != tt.refused || (tt.err != nil && err.Error() != tt.err.Error()) {
					t.Errorf("error = %v, want the text of %v, marked refused %v", err, tt.err, tt.refused)
				}
				if took := time.Since(start); took != tt.wantTook {
					t.Errorf("took %v, want exactly %v", took, tt.wantTook)
				}
				var want *account.Account
				if !tt.refused {
					want = &account.Account{
						Email: id.email, Username: id.username, Password: id.password,
						CreatedAt: start.UTC(), Proxy: testProxyAddr, Unconfirmed: tt.wantErr != nil,
					}
				}
				if !reflect.DeepEqual(acct, want) {
					t.Errorf("account = %+v, want %+v", acct, want)
				}
				wantFailures := 2
				if tt.wantErr == nil {
					wantFailures = 1
				}
				if _, failures, _ := mgr.ReportFailure(testProxy()); failures != wantFailures {
					t.Errorf("failures after the attempt = %d, want %d: only a created account reports a success", failures, wantFailures)
				}
				wantCalls, wantSteps := []string{post, wait}, "validate_name submit_password wait_account_creation"
				if tt.fail == "SubmitPassword" {
					wantCalls, wantSteps = []string{post}, "validate_name submit_password"
				}
				if !slices.Equal(reg.calls, wantCalls) {
					t.Errorf("calls = %q, want %q", reg.calls, wantCalls)
				}
				if got := stepNames(a.steps); got != wantSteps {
					t.Errorf("steps = %q, want %q", got, wantSteps)
				}
			})
		})
	}
}

func TestGenerateIdentity(t *testing.T) {
	cfg := &config.Config{Account: config.AccountConfig{PasswordLength: 20}, Email: config.EmailConfig{IMAPDomains: []string{"example.com"}}}

	t.Run("generates every field", func(t *testing.T) {
		var buf bytes.Buffer
		a := &attempt{run: &runner{cfg: cfg}, logger: logtest.New(&buf)}
		id := a.generateIdentity()
		if local, ok := strings.CutSuffix(id.email, "@example.com"); !ok || len(local) != 12 {
			t.Errorf("email = %q, want a 12-character local part at the domain of the config", id.email)
		}
		if id.dob == "" {
			t.Error("dob is empty")
		}
		if len(id.username) != 12 {
			t.Errorf("username = %q, want 12 characters", id.username)
		}
		if len(id.password) != cfg.Account.PasswordLength {
			t.Errorf("password length = %d, want %d", len(id.password), cfg.Account.PasswordLength)
		}
		for _, want := range []string{`msg="generated email"`, `msg="generated DOB"`, `msg="generated credentials" username=` + id.username} {
			if !strings.Contains(buf.String(), want) {
				t.Errorf("log has no %s:\n%s", want, buf.String())
			}
		}
	})
}
