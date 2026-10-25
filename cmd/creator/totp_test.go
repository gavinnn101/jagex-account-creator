package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"jagex_account_creator/internal/account"
	"jagex_account_creator/internal/config"
	"jagex_account_creator/internal/jagexapi"
	"jagex_account_creator/internal/logtest"
)

// fakeEnroller records the 2FA calls and returns fixed answers.
type fakeEnroller struct {
	loginErr    error
	setup       *jagexapi.TOTPSetupResponse
	setupErr    error
	backupCodes []string
	activateErr error

	calls       []string
	label       string
	activatedID string
	code        string
}

func (f *fakeEnroller) Login(context.Context) error {
	f.calls = append(f.calls, "Login")
	return f.loginErr
}

func (f *fakeEnroller) SetupTOTP(_ context.Context, email string) (*jagexapi.TOTPSetupResponse, error) {
	f.calls = append(f.calls, "SetupTOTP")
	f.label = email
	return f.setup, f.setupErr
}

func (f *fakeEnroller) ActivateTOTP(_ context.Context, totpID, verificationCode string) ([]string, error) {
	f.calls = append(f.calls, "ActivateTOTP")
	f.activatedID = totpID
	f.code = verificationCode
	return f.backupCodes, f.activateErr
}

func TestEnrollIfEnabled(t *testing.T) {
	const secret = "JBSWY3DPEHPK3PXP"
	setup := &jagexapi.TOTPSetupResponse{ID: "totp-1", Secret: secret}
	codes := []string{"B1", "B2"}

	tests := []struct {
		name            string
		disabled        bool
		enroller        fakeEnroller
		wantCalls       string
		wantSteps       string
		wantErr         string
		wantSecret      bool
		wantUnconfirmed bool // the activation failed, so the secret stays without backup codes
	}{
		{name: "disabled", disabled: true, enroller: fakeEnroller{setup: setup, backupCodes: codes}, wantSteps: "submit_password"},
		{
			name:       "enrolled",
			enroller:   fakeEnroller{setup: setup, backupCodes: codes},
			wantCalls:  "Login SetupTOTP ActivateTOTP",
			wantSteps:  "submit_password login setup_totp activate_totp",
			wantSecret: true,
		},
		{
			name:      "login fails",
			enroller:  fakeEnroller{loginErr: errors.New("login refused")},
			wantCalls: "Login",
			wantSteps: "submit_password login",
			wantErr:   `error="logging in: login refused"`,
		},
		{
			name:      "setup fails",
			enroller:  fakeEnroller{setupErr: errors.New("setup refused")},
			wantCalls: "Login SetupTOTP",
			wantSteps: "submit_password login setup_totp",
			wantErr:   `error="setup refused"`,
		},
		{
			name:      "secret does not decode",
			enroller:  fakeEnroller{setup: &jagexapi.TOTPSetupResponse{ID: "totp-1", Secret: "!!!"}},
			wantCalls: "Login SetupTOTP",
			wantSteps: "submit_password login setup_totp activate_totp",
			wantErr:   `error="generating TOTP code: decoding TOTP secret: `,
		},
		{
			name:      "a refused activation keeps no secret",
			enroller:  fakeEnroller{setup: setup, activateErr: fmt.Errorf("activating TOTP: %w: status 400", jagexapi.ErrRegistrationFailed)},
			wantCalls: "Login SetupTOTP ActivateTOTP",
			wantSteps: "submit_password login setup_totp activate_totp",
			wantErr:   `error="activating TOTP: account registration failed: status 400"`,
		},
		{
			name:      "a 429 on the activation keeps no secret",
			enroller:  fakeEnroller{setup: setup, activateErr: fmt.Errorf("activating TOTP: %w: status 429", jagexapi.ErrRateLimited)},
			wantCalls: "Login SetupTOTP ActivateTOTP",
			wantSteps: "submit_password login setup_totp activate_totp",
			wantErr:   `error="activating TOTP: rate limited: status 429"`,
		},
		{
			name:            "activation fails",
			enroller:        fakeEnroller{setup: setup, backupCodes: codes, activateErr: errors.New("bad code")},
			wantCalls:       "Login SetupTOTP ActivateTOTP",
			wantSteps:       "submit_password login setup_totp activate_totp",
			wantErr:         `error="bad code"`,
			wantUnconfirmed: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var buf bytes.Buffer
				logger := logtest.New(&buf)
				a := &attempt{
					run:    &runner{cfg: &config.Config{Account: config.AccountConfig{EnableTOTP: new(!tt.disabled)}}},
					logger: logger,
					steps:  newStepTracker(time.Now(), "submit_password", phaseHTTP),
				}
				acct := &account.Account{Email: "user@example.com", Username: "user"}
				enroller := tt.enroller

				a.enrollIfEnabled(t.Context(), &enroller, acct)

				if got := strings.Join(enroller.calls, " "); got != tt.wantCalls {
					t.Errorf("calls = %q, want %q", got, tt.wantCalls)
				}
				if got := stepNames(a.steps); got != tt.wantSteps {
					t.Errorf("steps = %q, want %q", got, tt.wantSteps)
				}
				if _, failed := a.steps.failedStep(); failed {
					t.Error("a 2FA failure marked a failed step, but the attempt still succeeds")
				}
				warn := `level=WARN msg="2FA enrollment failed, writing the account without it" email=user@example.com username=user `
				if tt.wantUnconfirmed {
					warn = `level=WARN msg="2FA activation unconfirmed, writing the account with its TOTP secret" email=user@example.com username=user `
				}
				if tt.wantErr != "" {
					if !strings.Contains(buf.String(), warn+tt.wantErr) {
						t.Errorf("log has no warning with %s:\n%s", tt.wantErr, buf.String())
					}
				} else if strings.Contains(buf.String(), "level=WARN") {
					t.Errorf("unexpected warning:\n%s", buf.String())
				}
				if acct.Unconfirmed != tt.wantUnconfirmed {
					t.Errorf("Unconfirmed = %v, want %v", acct.Unconfirmed, tt.wantUnconfirmed)
				}
				if tt.wantUnconfirmed {
					if acct.TOTPSecret != secret || acct.BackupCodes != nil {
						t.Errorf("TOTP fields = %q, %v, want %q and no backup codes", acct.TOTPSecret, acct.BackupCodes, secret)
					}
					return
				}
				if !tt.wantSecret {
					if acct.TOTPSecret != "" || acct.BackupCodes != nil {
						t.Errorf("TOTP fields = %q, %v, want empty", acct.TOTPSecret, acct.BackupCodes)
					}
					return
				}
				if acct.TOTPSecret != secret || strings.Join(acct.BackupCodes, ",") != "B1,B2" {
					t.Errorf("TOTP fields = %q, %v, want %q, %v", acct.TOTPSecret, acct.BackupCodes, secret, codes)
				}
				if enroller.label != acct.Email || enroller.activatedID != "totp-1" || len(enroller.code) != 6 {
					t.Errorf("label, id, code = %q, %q, %q, want the email, totp-1 and a 6-digit code", enroller.label, enroller.activatedID, enroller.code)
				}
			})
		})
	}

	limited := fmt.Errorf("%w: status 429", jagexapi.ErrRateLimited)
	for _, rl := range []struct {
		name     string
		enroller *fakeEnroller
		wantErr  string
	}{
		{"login", &fakeEnroller{loginErr: limited}, "logging in: rate limited: status 429"},
		{"activation", &fakeEnroller{setup: setup, activateErr: limited}, "rate limited: status 429"},
	} {
		t.Run("a rate limit on the "+rl.name+" cools the proxy and the account is still written", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				assertRateLimitWritesAccount(t, rl.enroller, rl.wantErr)
			})
		})
	}
}

// assertRateLimitWritesAccount enrolls through enroller, whose 429 fails with
// wantErr. It checks that the proxy cools and that the account is written without 2FA.
func assertRateLimitWritesAccount(t *testing.T, enroller *fakeEnroller, wantErr string) {
	t.Helper()
	s := newTestStore(t)
	var buf bytes.Buffer
	a := &attempt{
		run: &runner{
			cfg:      &config.Config{Account: config.AccountConfig{EnableTOTP: new(true)}},
			proxyMgr: proxyManager(t, testProxyAddr),
			history:  newTestHistory(t, s),
		},
		logger: logtest.New(&buf),
		p:      testProxy(),
		steps:  newStepTracker(time.Now(), "submit_password", phaseHTTP),
	}
	acct := &account.Account{Email: "user@example.com", Username: "user", Password: "pw", Proxy: testProxyAddr}

	a.enrollIfEnabled(t.Context(), enroller, acct)
	if err := a.finish(t.Context(), acct, nil, nil, nil); err != nil {
		t.Fatalf("finish: %v", err)
	}

	warn := `level=WARN msg="proxy in cooldown" reason="rate limited" proxy=` + testProxyAddr +
		` error="` + wantErr + `" cooldown=5m0s failures=1`
	if !strings.Contains(buf.String(), warn+"\n") {
		t.Errorf("log has no cooldown line %s:\n%s", warn, buf.String())
	}
	if !strings.Contains(buf.String(), `msg="account created" outcome=success`) {
		t.Errorf("log has no success line:\n%s", buf.String())
	}
	const want = `{"email":"user@example.com","username":"user","password":"pw","created_at":"0001-01-01T00:00:00Z","proxy":"` + testProxyAddr + `"}` + "\n"
	if got := readRecords(t, s); got != want {
		t.Errorf("export = %q, want %q: confirmed and without 2FA", got, want)
	}
	if got := nextWait(t, a.run.proxyMgr); got != 5*time.Minute {
		t.Errorf("Next waited %v, want exactly 5m0s: the proxy is in cooldown", got)
	}
}
