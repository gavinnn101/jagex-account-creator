package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"

	"github.com/mafredri/cdp"
	"github.com/mafredri/cdp/protocol/target"
	"github.com/mafredri/cdp/session"
)

// runIfWaitingForDebugger is the raw Runtime message that resumes a target
// that auto-attach paused.
const runIfWaitingForDebugger = `{"id":1,"method":"Runtime.runIfWaitingForDebugger"}`

// TargetAttacher pauses each new child target under a page, sets it up, then
// resumes it. The set-up sends the UA override and subscribes the network
// tracker, each when it is set. The attach recurses into each child.
// Limitation: a service worker or a shared worker is not attached from the page, so it sends Chrome's own UA.
type TargetAttacher struct {
	override *UserAgentOverride // nil: the children keep Chrome's own UA
	tracker  *NetworkTracker    // nil: no child traffic goes to a breakdown
	fail     context.CancelCauseFunc
	logger   *slog.Logger

	cancel    context.CancelFunc
	wg        sync.WaitGroup // every pump, and so every attach, which runs in a pump
	closeOnce sync.Once

	mu       sync.Mutex
	managers []io.Closer // the session.Manager of each target that auto-attaches its children
}

// NewTargetAttacher returns an attacher that sends override and subscribes
// tracker on each child target. Either can be nil. When a child cannot get
// override, the attacher calls fail with an error that wraps
// ErrUserAgentOverride.
func NewTargetAttacher(logger *slog.Logger, override *UserAgentOverride, tracker *NetworkTracker, fail context.CancelCauseFunc) *TargetAttacher {
	return &TargetAttacher{
		override: override,
		tracker:  tracker,
		fail:     fail,
		logger:   logger.With("component", "target-attacher"),
		cancel:   func() {}, // Start replaces it
	}
}

// Start turns on auto-attach for the page client c. Call Close also after an error.
func (a *TargetAttacher) Start(ctx context.Context, c *cdp.Client) error { // coverage-ignore: needs a live Chrome
	ctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	return a.autoAttach(ctx, c)
}

// autoAttach pauses each new child target of c until setUpTarget is done with
// it, then resumes it.
func (a *TargetAttacher) autoAttach(ctx context.Context, c *cdp.Client) error { // coverage-ignore: needs a live Chrome
	attached, err := c.Target.AttachedToTarget(ctx)
	if err != nil {
		return fmt.Errorf("subscribing to AttachedToTarget: %w", err)
	}
	mgr, err := session.NewManager(c)
	if err != nil {
		_ = attached.Close()
		return fmt.Errorf("creating the session manager: %w", err)
	}
	a.mu.Lock()
	a.managers = append(a.managers, mgr)
	a.mu.Unlock()

	setUp := func(info target.Info) error { return a.setUpTarget(ctx, mgr, info) }
	// Only a resume on the auto-attach session releases the frame.
	resume := func(id target.SessionID) error {
		args := target.NewSendMessageToTargetArgs(runIfWaitingForDebugger).SetSessionID(id)
		//nolint:staticcheck // Flat mode needs a session ID in rpcc, which v0.35.0 lacks, so the non-flat call stays.
		if err := c.Target.SendMessageToTarget(ctx, args); err != nil {
			return fmt.Errorf("resuming: %w", err)
		}
		return nil
	}
	// The attach runs in the pump, so the wait of Close covers it.
	a.wg.Go(func() {
		pump(attached.Close, attached.Recv, func(event *target.AttachedToTargetReply) {
			a.attached(ctx, event, setUp, resume)
		})
	})

	if err := c.Target.SetAutoAttach(ctx, target.NewSetAutoAttachArgs(true, true).SetFlatten(false)); err != nil {
		return fmt.Errorf("setting auto-attach: %w", err)
	}
	return nil
}

// setUpTarget dials the paused target, subscribes the tracker, enables Network,
// sends the UA override and auto-attaches the target's own children. The
// manager closes the connection.
func (a *TargetAttacher) setUpTarget(ctx context.Context, mgr *session.Manager, info target.Info) error { // coverage-ignore: needs a live Chrome
	conn, err := mgr.Dial(ctx, info.TargetID)
	if err != nil {
		return fmt.Errorf("dialing: %w", err)
	}
	child := cdp.NewClient(conn)
	// The subscription comes before Network.Enable, so it sees the first event.
	if a.tracker != nil {
		if err := a.tracker.track(ctx, child.Network); err != nil {
			a.report(info, err)
		}
	}
	if err := child.Network.Enable(ctx, nil); err != nil {
		return fmt.Errorf("enabling Network: %w", err)
	}
	if a.override != nil {
		if err := setUserAgentOverride(ctx, conn, a.override); err != nil {
			return err
		}
	}
	return a.autoAttach(ctx, child)
}

// attached sets up a target that auto-attach paused, then resumes it. With an
// override, a set-up error fails the browser phase after the resume, unless ctx,
// the attach context, is done. Every other error goes to report.
func (a *TargetAttacher) attached(ctx context.Context, event *target.AttachedToTargetReply, setUp func(target.Info) error, resume func(target.SessionID) error) {
	// Our own Dial raises a second attachedToTarget with waitingForDebugger=false. A handler for it would attach without end.
	// Limitation: a target that existed before SetAutoAttach is skipped too. The attacher starts on about:blank, so none exists.
	if !event.WaitingForDebugger {
		return
	}
	info := event.TargetInfo
	setUpErr := setUp(info)
	// A paused target stalls its frame, so every path resumes it.
	resumeErr := resume(event.SessionID)
	// After Close or a parent cancel, the set-up error is that cancel, not a missing override.
	if setUpErr != nil && a.override != nil && ctx.Err() == nil {
		// Limitation: a child that closes during its set-up also fails the browser phase.
		a.fail(fmt.Errorf("%w: %s target %s: %w", ErrUserAgentOverride, info.Type, info.TargetID, setUpErr))
		setUpErr = nil
	}
	if err := errors.Join(setUpErr, resumeErr); err != nil {
		a.report(info, err)
	}
}

// report records an attach error that does not fail the browser phase. The
// tracker's breakdown lists it, else a DEBUG line logs it.
func (a *TargetAttacher) report(info target.Info, err error) {
	err = fmt.Errorf("%s target %s: %w", info.Type, info.TargetID, err)
	if a.tracker != nil {
		a.tracker.addAttachError(err)
		return
	}
	a.logger.Debug("child target attach failed", "error", err)
}

// Close stops the attach, waits for the attaches in progress and detaches from
// every child target. Safe to call multiple times, before Start, and on a nil
// attacher.
// Limitation: auto-attach stays on, so a frame created after Close waits for a
// resume that never comes. Callers close the attacher only when the page is done.
func (a *TargetAttacher) Close() {
	if a == nil {
		return
	}
	a.closeOnce.Do(func() {
		a.cancel()
		// Each attach runs in a pump, so after the wait no manager can be added.
		a.wg.Wait()

		a.mu.Lock()
		defer a.mu.Unlock()
		// A manager closes the connections that it dialed. A child closes before its parent.
		for _, m := range slices.Backward(a.managers) {
			_ = m.Close()
		}
	})
}
