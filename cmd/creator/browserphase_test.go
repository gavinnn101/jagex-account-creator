package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/mafredri/cdp"
	"github.com/mafredri/cdp/protocol/page"

	"jagex_account_creator/internal/browser"
	"jagex_account_creator/internal/browser/cdptest"
	"jagex_account_creator/internal/jagexapi"
	"jagex_account_creator/internal/logtest"
)

func TestBrowserPhaseError(t *testing.T) {
	overrideErr := fmt.Errorf("%w: iframe target T1: enabling Network: target closed", browser.ErrUserAgentOverride)
	waitErr := fmt.Errorf("waiting for clearance: %w", browser.ErrChallengeTimeout)
	const overrideText = "stopping the browser phase: a child target has no user agent override: iframe target T1: enabling Network: target closed"
	const waitText = "waiting for clearance: cloudflare challenge timed out"
	failChild := func(_ context.CancelFunc, child context.CancelCauseFunc) { child(overrideErr) }
	parentFirst := func(parent context.CancelFunc, child context.CancelCauseFunc) {
		parent()
		child(overrideErr) // too late: the parent cancel set the cause
	}
	tests := []struct {
		name         string
		err          error
		cancel       func(parent context.CancelFunc, child context.CancelCauseFunc)
		want         error
		wantText     string // "" for a nil error
		wantCategory string // the category that the run logs with the parent context
	}{
		{"no cancel", waitErr, func(context.CancelFunc, context.CancelCauseFunc) {}, waitErr, waitText, "challenge_timeout"},
		{"override cause replaces the error", fmt.Errorf("waiting for clearance: %w", context.Canceled), failChild, overrideErr, overrideText, "ua_override"},
		{"an override cause fails a success", nil, failChild, overrideErr, overrideText, "ua_override"},
		{"parent cancel keeps the error", waitErr, parentFirst, waitErr, waitText, "cancelled"},
		{"a parent cancel keeps a success", nil, parentFirst, nil, "", ""},
		{"other cause keeps the error", waitErr, func(_ context.CancelFunc, child context.CancelCauseFunc) { child(errors.New("other")) }, waitErr, waitText, "challenge_timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent, cancelParent := context.WithCancel(t.Context())
			defer cancelParent()
			ctx, cancel := context.WithCancelCause(parent)
			defer cancel(nil)
			tt.cancel(cancelParent, cancel)

			got := browserPhaseError(ctx, tt.err)
			gotText, gotCategory := "", ""
			if got != nil {
				gotText, gotCategory = got.Error(), failureCategory(parent, got)
			}
			if !errors.Is(got, tt.want) || gotText != tt.wantText {
				t.Errorf("browserPhaseError = %v, want %q", got, tt.wantText)
			}
			if gotCategory != tt.wantCategory {
				t.Errorf("failureCategory = %q, want %q", gotCategory, tt.wantCategory)
			}
		})
	}
}

func TestNavigateToRegistration(t *testing.T) {
	const navigating = `level=DEBUG msg="navigating to registration page"` + "\n"
	const tunnelFailed = "net::ERR_TUNNEL_CONNECTION_FAILED"
	regURL := jagexapi.ManageRegistrationURL
	navErr := errors.New("navigate refused")
	mapped := errors.New("proxy mapped")
	tests := []struct {
		name        string
		navError    string // the net error of the navigation, or ""
		navErr      error  // the error of the Navigate call
		noLoad      bool   // whether LoadEventFired never fires
		mapProxy    bool   // whether onProxyFailure is set
		wantErr     string
		wantIs      error
		wantMapped  int // the calls of onProxyFailure
		wantElapsed time.Duration
		wantLog     string
	}{
		{name: "loaded", mapProxy: true, wantLog: navigating},
		{
			name:        "a load timeout only warns",
			noLoad:      true,
			mapProxy:    true,
			wantElapsed: 30 * time.Second,
			wantLog:     navigating + fmt.Sprintf(`level=WARN msg="page load timed out, continuing anyway" url=%q`, regURL) + "\n",
		},
		{
			name:       "a proxy failure goes to onProxyFailure",
			navError:   tunnelFailed,
			mapProxy:   true,
			wantErr:    mapped.Error(),
			wantIs:     mapped,
			wantMapped: 1,
			wantLog:    navigating,
		},
		{
			name:     "a proxy failure with no onProxyFailure",
			navError: tunnelFailed,
			wantErr:  "navigation error for " + regURL + ": " + tunnelFailed + ": " + browser.ErrProxyConnection.Error(),
			wantIs:   browser.ErrProxyConnection,
			wantLog:  navigating,
		},
		{
			name:     "any other error is returned unchanged",
			navErr:   navErr,
			mapProxy: true,
			wantErr:  "navigating to " + regURL + ": navigate refused",
			wantIs:   navErr,
			wantLog:  navigating,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				loads := []*page.LoadEventFiredReply{{}}
				if tt.noLoad {
					loads = nil
				}
				p := &cdptest.Page{
					DOMContent:  cdptest.NewStream(&page.DOMContentEventFiredReply{}),
					Load:        cdptest.NewStream(loads...),
					NavigateErr: tt.navErr,
				}
				if tt.navError != "" {
					p.Navigation.ErrorText = &tt.navError
				}
				var proxyErrs []error
				var onProxyFailure func(error) error
				if tt.mapProxy {
					onProxyFailure = func(err error) error {
						proxyErrs = append(proxyErrs, err)
						return mapped
					}
				}
				var buf bytes.Buffer
				start := time.Now()

				err := navigateToRegistration(t.Context(), &cdp.Client{Page: p}, logtest.New(&buf), onProxyFailure)

				gotErr := ""
				if err != nil {
					gotErr = err.Error()
				}
				if gotErr != tt.wantErr || !errors.Is(err, tt.wantIs) {
					t.Errorf("navigateToRegistration() error = %v, want %q", err, tt.wantErr)
				}
				if len(proxyErrs) != tt.wantMapped || (tt.wantMapped > 0 && !errors.Is(proxyErrs[0], browser.ErrProxyConnection)) {
					t.Errorf("onProxyFailure got %v, want %d proxy connection errors", proxyErrs, tt.wantMapped)
				}
				if elapsed := time.Since(start); elapsed != tt.wantElapsed {
					t.Errorf("navigateToRegistration() took %v, want %v", elapsed, tt.wantElapsed)
				}
				if buf.String() != tt.wantLog {
					t.Errorf("log = %q, want %q", buf.String(), tt.wantLog)
				}
				if want := []string{regURL}; !slices.Equal(p.URLs, want) {
					t.Errorf("navigations = %q, want %q", p.URLs, want)
				}
			})
		})
	}
}

func TestFinishTargets_NoSetUp(t *testing.T) {
	var buf bytes.Buffer
	(&browserPhase{}).finishTargets(logtest.New(&buf))
	if buf.Len() != 0 {
		t.Errorf("a phase with no tracker logged:\n%s", buf.String())
	}
}

func TestFinishTargets_Once(t *testing.T) {
	var buf bytes.Buffer
	b := &browserPhase{tracker: &browser.NetworkTracker{}}

	b.finishTargets(logtest.New(&buf))
	b.finishTargets(logtest.New(&buf))

	if n := strings.Count(buf.String(), `msg="browser bandwidth breakdown"`); n != 1 {
		t.Errorf("log has %d breakdowns, want 1:\n%s", n, buf.String())
	}
}
