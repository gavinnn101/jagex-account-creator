package browser

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/mafredri/cdp/protocol/target"

	"jagex_account_creator/internal/logtest"
)

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

func TestAttacherCloseClosesManagersChildFirst(t *testing.T) {
	a := NewTargetAttacher(slog.Default(), nil, nil, nil)
	var closed []string
	for _, name := range []string{"page", "frame", "nested"} {
		a.managers = append(a.managers, closerFunc(func() error {
			closed = append(closed, name)
			return errors.New("ignored")
		}))
	}

	a.Close()
	a.Close()

	if want := []string{"nested", "frame", "page"}; !slices.Equal(closed, want) {
		t.Errorf("closed %v, want %v", closed, want)
	}
}

// TestAttacherCloseWaitsForAttach holds an attach in its pump while Close
// starts. Close must wait for it and then close the manager that the attach added.
func TestAttacherCloseWaitsForAttach(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := NewTargetAttacher(slog.Default(), nil, nil, nil)
		canceled := make(chan struct{})
		a.cancel = func() { close(canceled) }
		var managerClosed atomic.Bool
		a.wg.Go(func() {
			<-canceled
			a.mu.Lock()
			a.managers = append(a.managers, closerFunc(func() error { managerClosed.Store(true); return nil }))
			a.mu.Unlock()
		})

		a.Close()

		if !managerClosed.Load() {
			t.Error("Close returned without closing the manager that the attach added")
		}
	})
}

func TestNilAttacherClose(_ *testing.T) {
	var a *TargetAttacher
	a.Close()
}

// attachedCase is one run of TargetAttacher.attached with fake steps.
type attachedCase struct {
	name        string
	override    bool
	tracker     bool
	waiting     bool
	closeInSet  bool // Close runs during the set-up
	setUpErr    error
	resumeErr   error
	wantCalls   []string
	wantErrors  []string // the errors that report records
	wantFailure string   // the cause passed to fail, or empty for no call
}

func TestAttached(t *testing.T) {
	errSetUp := errors.New("dialing: gone")
	errResume := errors.New("resuming: closed")
	both := []string{"set up T1", "resume S1"}
	tests := []attachedCase{
		{name: "paused target", override: true, tracker: true, waiting: true, wantCalls: both},
		{name: "our own Dial", override: true, tracker: true},
		{
			name: "set-up fails with an override", override: true, tracker: true, waiting: true, setUpErr: errSetUp,
			wantCalls:   append(slices.Clone(both), "fail"),
			wantFailure: "a child target has no user agent override: iframe target T1: dialing: gone",
		},
		{
			name: "set-up fails without an override", tracker: true, waiting: true, setUpErr: errSetUp,
			wantCalls:  both,
			wantErrors: []string{"iframe target T1: dialing: gone"},
		},
		{
			name: "resume fails", override: true, tracker: true, waiting: true, resumeErr: errResume,
			wantCalls:  both,
			wantErrors: []string{"iframe target T1: resuming: closed"},
		},
		{
			name: "both fail without an override", tracker: true, waiting: true, setUpErr: errSetUp, resumeErr: errResume,
			wantCalls:  both,
			wantErrors: []string{"iframe target T1: dialing: gone\nresuming: closed"},
		},
		{
			name: "both fail with an override", override: true, tracker: true, waiting: true, setUpErr: errSetUp, resumeErr: errResume,
			wantCalls:   append(slices.Clone(both), "fail"),
			wantErrors:  []string{"iframe target T1: resuming: closed"},
			wantFailure: "a child target has no user agent override: iframe target T1: dialing: gone",
		},
		{
			name: "resume fails with no tracker", override: true, waiting: true, resumeErr: errResume,
			wantCalls:  both,
			wantErrors: []string{"iframe target T1: resuming: closed"},
		},
		{
			name: "Close during the set-up", override: true, tracker: true, waiting: true, closeInSet: true,
			setUpErr:   fmt.Errorf("enabling Network: %w", context.Canceled),
			wantCalls:  both,
			wantErrors: []string{"iframe target T1: enabling Network: context canceled"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runAttached(t, tt)
		})
	}
}

// runAttached runs one attached case. With no tracker, the reported errors come
// from the DEBUG log.
func runAttached(t *testing.T, tt attachedCase) {
	t.Helper()
	var calls []string
	var failure error
	fail := func(cause error) { calls = append(calls, "fail"); failure = cause }
	var override *UserAgentOverride
	if tt.override {
		override = testOverride(measuredHeadfulUA)
	}
	var tracker *NetworkTracker
	if tt.tracker {
		tracker = newTestTracker()
	}
	var log strings.Builder
	logger := logtest.New(&log)
	a := NewTargetAttacher(logger, override, tracker, fail)
	// The attach context, as Start derives it.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	a.cancel = cancel
	setUp := func(info target.Info) error {
		calls = append(calls, "set up "+string(info.TargetID))
		if tt.closeInSet {
			a.Close()
		}
		return tt.setUpErr
	}
	resume := func(id target.SessionID) error { calls = append(calls, "resume "+string(id)); return tt.resumeErr }
	event := &target.AttachedToTargetReply{
		SessionID:          "S1",
		TargetInfo:         target.Info{TargetID: "T1", Type: "iframe"},
		WaitingForDebugger: tt.waiting,
	}

	a.attached(ctx, event, setUp, resume)

	if !slices.Equal(calls, tt.wantCalls) {
		t.Errorf("calls = %v, want %v", calls, tt.wantCalls)
	}
	if tt.wantFailure != "" && (failure == nil || failure.Error() != tt.wantFailure || !errors.Is(failure, ErrUserAgentOverride) || !errors.Is(failure, tt.setUpErr)) {
		t.Errorf("fail cause = %v, want %q that wraps ErrUserAgentOverride and the set-up error", failure, tt.wantFailure)
	}
	if got := reportedErrors(tracker, log.String()); !slices.Equal(got, tt.wantErrors) {
		t.Errorf("reported errors = %q, want %q", got, tt.wantErrors)
	}
}

// reportedErrors returns the attach errors in the tracker's breakdown, or the
// error attributes of the DEBUG lines when there is no tracker.
func reportedErrors(tracker *NetworkTracker, log string) []string {
	var got []string
	if tracker != nil {
		for _, err := range tracker.summary().AttachErrors {
			got = append(got, err.Error())
		}
		return got
	}
	for line := range strings.Lines(log) {
		if _, after, ok := strings.Cut(line, `msg="child target attach failed" component=target-attacher error="`); ok {
			got = append(got, strings.TrimSuffix(strings.TrimSpace(after), `"`))
		}
	}
	return got
}
