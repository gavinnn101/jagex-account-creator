package browser

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/mafredri/cdp"
	"github.com/mafredri/cdp/protocol/page"
	"github.com/mafredri/cdp/protocol/runtime"
	"github.com/mafredri/cdp/rpcc"

	"jagex_account_creator/internal/browser/cdptest"
	"jagex_account_creator/internal/logtest"
)

func TestIsProxyNetError(t *testing.T) {
	tests := []struct {
		errText string
		want    bool
	}{
		{"net::ERR_TUNNEL_CONNECTION_FAILED", true},
		{"net::ERR_PROXY_CONNECTION_FAILED", true},
		{"net::ERR_CONNECTION_REFUSED", false},
		{"net::ERR_NAME_NOT_RESOLVED", false},
		{"net::ERR_TIMED_OUT", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.errText, func(t *testing.T) {
			got := isProxyNetError(tt.errText)
			if got != tt.want {
				t.Errorf("isProxyNetError(%q) = %v, want %v", tt.errText, got, tt.want)
			}
		})
	}
}

const testPageURL = "https://example.com/manage"

// loadingPage returns a fake Page whose DOMContentEventFired has fired and
// whose LoadEventFired fires when loaded is true. Call it in the synctest bubble.
func loadingPage(loaded bool) *cdptest.Page {
	p := &cdptest.Page{
		DOMContent: cdptest.NewStream(&page.DOMContentEventFiredReply{}),
		Load:       cdptest.NewStream[page.LoadEventFiredReply](),
	}
	if loaded {
		p.Load = cdptest.NewStream(&page.LoadEventFiredReply{})
	}
	return p
}

// closedStream returns a stream that is already closed.
func closedStream[T any]() *cdptest.Stream[T] {
	s := cdptest.NewStream[T]()
	_ = s.Close()
	return s
}

func TestNavigateAndLoad(t *testing.T) {
	tests := []struct {
		name        string
		page        func() *cdptest.Page
		cancel      time.Duration // the parent ends at this offset, or never when zero
		wantErr     string
		wantIs      error
		wantElapsed time.Duration
		wantURLs    int
	}{
		{
			name:    "DOMContentEventFired listener fails",
			page:    func() *cdptest.Page { return &cdptest.Page{DOMContentErr: errFake} },
			wantErr: "creating DOMContentEventFired listener: fake failure",
			wantIs:  errFake,
		},
		{
			name: "LoadEventFired listener fails",
			page: func() *cdptest.Page {
				p := loadingPage(true)
				p.LoadErr = errFake
				return p
			},
			wantErr: "creating LoadEventFired listener: fake failure",
			wantIs:  errFake,
		},
		{
			name: "navigation fails",
			page: func() *cdptest.Page {
				p := loadingPage(true)
				p.NavigateErr = errFake
				return p
			},
			wantErr:  "navigating to " + testPageURL + ": fake failure",
			wantIs:   errFake,
			wantURLs: 1,
		},
		{
			name:     "proxy net error",
			page:     func() *cdptest.Page { return navErrorPage("net::ERR_TUNNEL_CONNECTION_FAILED") },
			wantErr:  "navigation error for " + testPageURL + ": net::ERR_TUNNEL_CONNECTION_FAILED: " + ErrProxyConnection.Error(),
			wantIs:   ErrProxyConnection,
			wantURLs: 1,
		},
		{
			name:     "site net error",
			page:     func() *cdptest.Page { return navErrorPage("net::ERR_NAME_NOT_RESOLVED") },
			wantErr:  "navigation error for " + testPageURL + ": net::ERR_NAME_NOT_RESOLVED",
			wantURLs: 1,
		},
		{
			name: "DOMContentEventFired stream closes",
			page: func() *cdptest.Page {
				p := loadingPage(true)
				p.DOMContent = closedStream[page.DOMContentEventFiredReply]()
				return p
			},
			wantErr:  "waiting for DOMContentEventFired on " + testPageURL + ": rpcc: the stream is closing",
			wantIs:   rpcc.ErrStreamClosing,
			wantURLs: 1,
		},
		{
			name: "LoadEventFired stream closes",
			page: func() *cdptest.Page {
				p := loadingPage(false)
				p.Load = closedStream[page.LoadEventFiredReply]()
				return p
			},
			wantErr:  "waiting for LoadEventFired on " + testPageURL + ": rpcc: the stream is closing",
			wantIs:   rpcc.ErrStreamClosing,
			wantURLs: 1,
		},
		{name: "loaded", page: func() *cdptest.Page { return loadingPage(true) }, wantURLs: 1},
		{
			name:        "load timeout",
			page:        func() *cdptest.Page { return loadingPage(false) },
			wantErr:     ErrLoadTimeout.Error(),
			wantIs:      ErrLoadTimeout,
			wantElapsed: 2500 * time.Millisecond,
			wantURLs:    1,
		},
		{
			name:        "parent canceled during the load",
			page:        func() *cdptest.Page { return loadingPage(false) },
			cancel:      1500 * time.Millisecond,
			wantErr:     "waiting for LoadEventFired on " + testPageURL + ": context canceled",
			wantIs:      context.Canceled,
			wantElapsed: 1500 * time.Millisecond,
			wantURLs:    1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := tt.page()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if tt.cancel > 0 {
					time.AfterFunc(tt.cancel, cancel)
				}

				start := time.Now()
				err := NavigateAndLoad(ctx, &cdp.Client{Page: p}, testPageURL, 2500*time.Millisecond)

				if errString(err) != tt.wantErr || (tt.wantIs != nil && !errors.Is(err, tt.wantIs)) {
					t.Errorf("NavigateAndLoad() error = %v, want %q", err, tt.wantErr)
				}
				if elapsed := time.Since(start); elapsed != tt.wantElapsed {
					t.Errorf("NavigateAndLoad() took %v, want %v", elapsed, tt.wantElapsed)
				}
				if want := slices.Repeat([]string{testPageURL}, tt.wantURLs); !slices.Equal(p.URLs, want) {
					t.Errorf("navigations = %q, want %q", p.URLs, want)
				}
			})
		})
	}
}

// navErrorPage returns a loading fake Page whose navigation fails with the net error errText.
func navErrorPage(errText string) *cdptest.Page {
	p := loadingPage(true)
	p.Navigation.ErrorText = &errText
	return p
}

// pollingRuntime returns a fake Runtime whose JS condition is met from the
// read with index metAt. The read with index failAt fails.
func pollingRuntime(metAt, failAt int) *cdptest.Runtime {
	rt := &cdptest.Runtime{}
	rt.Answer = func(string) (*runtime.EvaluateReply, error) {
		read := len(rt.Calls) - 1
		if read == failAt {
			return nil, errFake
		}
		if read >= metAt {
			return cdptest.Value(`true`), nil
		}
		return cdptest.Value(`false`), nil
	}
	return rt
}

func TestWaitForJS(t *testing.T) {
	tests := []struct {
		name        string
		metAt       int
		failAt      int
		wantErr     error
		wantElapsed time.Duration
		wantReads   int
	}{
		{name: "met at once", metAt: 0, failAt: -1, wantReads: 1},
		{name: "met on the second poll", metAt: 2, failAt: -1, wantElapsed: 400 * time.Millisecond, wantReads: 3},
		// An evaluation error reads as not met, so the read that fails is not a match.
		{name: "a failed read", metAt: 0, failAt: 0, wantElapsed: 200 * time.Millisecond, wantReads: 2},
		{name: "never met", metAt: 100, failAt: -1, wantErr: context.DeadlineExceeded, wantElapsed: 1100 * time.Millisecond, wantReads: 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rt := pollingRuntime(tt.metAt, tt.failAt)

				start := time.Now()
				err := WaitForJS(t.Context(), &cdp.Client{Runtime: rt}, "cond", 1100*time.Millisecond)

				if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil) != (err == nil) {
					t.Errorf("WaitForJS() error = %v, want %v", err, tt.wantErr)
				}
				if elapsed := time.Since(start); elapsed != tt.wantElapsed || len(rt.Calls) != tt.wantReads {
					t.Errorf("WaitForJS() took %v and %d reads, want %v and %d", elapsed, len(rt.Calls), tt.wantElapsed, tt.wantReads)
				}
			})
		})
	}
}

func TestWaitForJSTimeoutText(t *testing.T) {
	tests := []struct {
		name        string
		cancelAfter time.Duration // 0 keeps the parent alive
		want        string
	}{
		{"timeout", 0, "waiting for the JS condition: context deadline exceeded"},
		{"parent cancel", 300 * time.Millisecond, "waiting for the JS condition: context canceled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parent, cancel := context.WithCancel(t.Context())
				defer cancel()
				if tt.cancelAfter > 0 {
					time.AfterFunc(tt.cancelAfter, cancel)
				}

				err := WaitForJS(parent, &cdp.Client{Runtime: pollingRuntime(100, -1)}, "cond", 1100*time.Millisecond)

				if errString(err) != tt.want {
					t.Errorf("WaitForJS() error = %v, want %q", err, tt.want)
				}
			})
		})
	}
}

func TestLogPageState(t *testing.T) {
	tests := []struct {
		name  string
		reply *runtime.EvaluateReply
		err   error
		want  string
	}{
		{"state read", cdptest.Value(`"{\"url\":\"u\"}"`), nil, `level=DEBUG msg="page state" label=probe data="{\"url\":\"u\"}"` + "\n"},
		{"read fails", nil, errFake, `level=WARN msg="could not get page state" label=probe error="evaluating JS: fake failure"` + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer

			LogPageState(t.Context(), &cdp.Client{Runtime: staticRuntime(tt.reply, tt.err)}, logtest.New(&buf), "probe")

			if buf.String() != tt.want {
				t.Errorf("log = %q, want %q", buf.String(), tt.want)
			}
		})
	}
}
