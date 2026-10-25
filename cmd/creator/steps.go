package main

import (
	"log/slog"
	"time"
)

// The coarse phases of an attempt. Each phase has its own forward proxy.
const (
	phaseBrowser = "browser"
	phaseHTTP    = "http"
)

// stepSlotWait is the first step of an attempt: the wait for a free browser slot.
const stepSlotWait = "browser_slot_wait"

// step is one timed step of an attempt.
type step struct {
	name     string
	phase    string
	duration time.Duration
}

// stepTracker times the steps of one attempt. A step runs until the next step
// begins or the attempt finishes, so the step durations add up to the total.
type stepTracker struct {
	start   time.Time
	opened  time.Time // the start of the last step
	steps   []step
	failed  int                                    // the index of the failed step, or -1
	onBegin func(name, phase string, at time.Time) // nil, or called with each step that begin opens
}

// newStepTracker starts the clock of an attempt at start, with its first step
// open since start.
func newStepTracker(start time.Time, name, phase string) *stepTracker {
	return &stepTracker{start: start, opened: start, steps: []step{{name: name, phase: phase}}, failed: -1}
}

// begin ends the open step and opens the step name of phase. It passes the
// new step and its start to onBegin when that is set. A nil tracker does nothing.
func (t *stepTracker) begin(name, phase string) {
	if t == nil {
		return
	}
	now := time.Now()
	t.end(now)
	t.steps = append(t.steps, step{name: name, phase: phase})
	t.opened = now
	if t.onBegin != nil {
		t.onBegin(name, phase, now)
	}
}

// end sets the duration of the open step to the time from its start to now.
func (t *stepTracker) end(now time.Time) {
	t.steps[len(t.steps)-1].duration = now.Sub(t.opened)
}

// fail marks the open step as the failed step. Only the first call counts.
func (t *stepTracker) fail() {
	if t.failed < 0 {
		t.failed = len(t.steps) - 1
	}
}

// finish ends the open step and returns the total duration of the attempt.
func (t *stepTracker) finish() time.Duration {
	now := time.Now()
	t.end(now)
	return now.Sub(t.start)
}

// failedStep returns the failed step. It returns false when no step failed.
func (t *stepTracker) failedStep() (step, bool) {
	if t.failed < 0 {
		return step{}, false
	}
	return t.steps[t.failed], true
}

// group returns the step durations as one log group.
func (t *stepTracker) group() slog.Attr {
	attrs := make([]slog.Attr, len(t.steps))
	for i, s := range t.steps {
		attrs[i] = slog.Duration(s.name, s.duration.Round(time.Millisecond))
	}
	return slog.GroupAttrs("steps", attrs...)
}
