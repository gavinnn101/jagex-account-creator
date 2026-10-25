package main

import (
	"maps"
	"slices"
	"sync"
	"time"

	"jagex_account_creator/internal/api"
	"jagex_account_creator/internal/proxy"
)

// runState is the live state of one run of serve. The run calls its marks at
// each event, and GET /api/run reads a snapshot. One mutex guards every field.
// Each mark publishes its snapshot under it, so the events keep the order of
// the marks. No method calls out under it to anything other than the hub.
// A mark replaces the wait or the attempt of a row and never changes it in
// place, so a snapshot shares them with no copy.
type runState struct {
	mu           sync.Mutex
	events       *hub[api.RunState]
	phase        api.RunStatePhase
	runID        *int64 // nil until the run row exists
	startedAt    time.Time
	endedAt      *time.Time
	err          *string
	accountCount int
	workers      []api.RunWorker
	cooldowns    []api.RunCooldown
	// Attempts counts at the attempt end, so a slot wait that a stop ends is no attempt.
	counts api.RunCounts
}

// newRunState returns the state of a run that starts now and creates
// accountCount accounts. It publishes the first snapshot on events.
func newRunState(accountCount int, events *hub[api.RunState]) *runState {
	s := &runState{
		events:       events,
		phase:        api.RunPhaseRunning,
		startedAt:    time.Now(),
		accountCount: accountCount,
		workers:      []api.RunWorker{},
		cooldowns:    []api.RunCooldown{},
		counts:       api.RunCounts{Failures: map[string]int{}},
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publish()
	return s
}

// start creates one idle row for each of the n workers. A nil state does nothing.
func (s *runState) start(n int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workers = make([]api.RunWorker, n)
	for i := range s.workers {
		s.workers[i].Worker = i
	}
	s.publish()
}

// runRecorded sets id, the id of the run row in the run history. A nil state does nothing.
func (s *runState) runRecorded(id int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runID = new(id)
	s.publish()
}

// publish sends the snapshot to the subscribers of the events. The caller holds s.mu.
func (s *runState) publish() {
	s.events.publish(s.lockedSnapshot())
}

// subscribe returns the snapshot and a subscription to each later one, both
// under one lock, so no mark falls between them.
func (s *runState) subscribe() (api.RunState, <-chan api.RunState, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, cancel := s.events.subscribe()
	return s.lockedSnapshot(), ch, cancel
}

// proxyCooled records a cooldown of the proxy hostPort that started on reason
// and runs until until. A nil state does nothing.
func (s *runState) proxyCooled(hostPort, reason string, until time.Time, failures int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneCooldowns(time.Now())
	s.cooldowns = append(s.cooldowns, api.RunCooldown{Proxy: hostPort, Reason: reason, Until: until, Failures: failures})
	s.publish()
}

// pruneCooldowns drops each cooldown that ended at or before now.
// No path ends a cooldown early, so the end time alone decides.
func (s *runState) pruneCooldowns(now time.Time) {
	s.cooldowns = slices.DeleteFunc(s.cooldowns, func(c api.RunCooldown) bool { return !now.Before(c.Until) })
}

// stop moves a running run to the stop stage: stopping, or stopping_now when
// now is true. A run that has ended keeps its phase. Only a change of the phase publishes.
func (s *runState) stop(now bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	was := s.phase
	switch {
	case now && (s.phase == api.RunPhaseRunning || s.phase == api.RunPhaseStopping):
		s.phase = api.RunPhaseStoppingNow
	case s.phase == api.RunPhaseRunning:
		s.phase = api.RunPhaseStopping
	}
	if s.phase != was {
		s.publish()
	}
}

// end records the end of the run and the error that it returned.
func (s *runState) end(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase = api.RunPhaseEnded
	s.endedAt = new(time.Now())
	if err != nil {
		s.err = new(err.Error())
	}
	s.publish()
}

// snapshot returns a copy of the state. A nil state is the idle state before
// the first run. Workers, Cooldowns and Failures are never nil, so the JSON holds no null.
func (s *runState) snapshot() api.RunState {
	if s == nil {
		return api.RunState{
			Phase:     api.RunPhaseIdle,
			Workers:   []api.RunWorker{},
			Cooldowns: []api.RunCooldown{},
			Counts:    api.RunCounts{Failures: map[string]int{}},
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lockedSnapshot()
}

// lockedSnapshot returns a copy of the state. The caller holds s.mu.
func (s *runState) lockedSnapshot() api.RunState {
	s.pruneCooldowns(time.Now())
	counts := s.counts
	counts.Failures = maps.Clone(s.counts.Failures)
	return api.RunState{
		Phase:        s.phase,
		RunId:        s.runID,
		StartedAt:    new(s.startedAt),
		EndedAt:      s.endedAt,
		Error:        s.err,
		AccountCount: new(s.accountCount),
		Workers:      append(make([]api.RunWorker, 0, len(s.workers)), s.workers...),
		Cooldowns:    append(make([]api.RunCooldown, 0, len(s.cooldowns)), s.cooldowns...),
		Counts:       counts,
	}
}

// workerState is the handle of one worker on the run state. A handle with a
// nil state does nothing, as stepTracker.begin does with a nil tracker. The CLI
// passes such handles.
type workerState struct {
	state  *runState
	worker int
}

// update runs change on the row of the worker under the lock of the state, and publishes the result.
func (w workerState) update(change func(row *api.RunWorker)) {
	if w.state == nil {
		return
	}
	w.state.mu.Lock()
	defer w.state.mu.Unlock()
	change(&w.state.workers[w.worker])
	w.state.publish()
}

// waitProxy marks the start of the wait for a proxy.
func (w workerState) waitProxy() {
	now := time.Now()
	w.update(func(row *api.RunWorker) {
		row.Wait, row.Attempt = &api.RunWait{Kind: api.RunWaitProxy, Since: now}, nil
	})
}

// attemptStarted marks an attempt through p that started at start. Its open
// step is the wait for a browser slot. The zero Proxy is a direct connection.
func (w workerState) attemptStarted(p proxy.Proxy, start time.Time) {
	a := &api.RunAttempt{StartedAt: start, Step: stepSlotWait, StepPhase: phaseBrowser, StepStartedAt: start}
	if !p.IsZero() {
		a.Proxy = new(p.HostPort())
	}
	w.update(func(row *api.RunWorker) { row.Wait, row.Attempt = nil, a })
}

// stepStarted marks the step name of phase in the attempt of the worker, which
// started at start.
func (w workerState) stepStarted(name, phase string, start time.Time) {
	w.update(func(row *api.RunWorker) {
		// Limitation: a begin after the end of the attempt finds no attempt, and the dereference panics.
		a := *row.Attempt
		a.Step, a.StepPhase, a.StepStartedAt = name, phase, start
		row.Attempt = &a
	})
}

// backoff marks the start of a backoff that lasts delay.
func (w workerState) backoff(delay time.Duration) {
	now := time.Now()
	w.update(func(row *api.RunWorker) {
		row.Wait, row.Attempt = &api.RunWait{Kind: api.RunWaitBackoff, Since: now, Until: new(now.Add(delay))}, nil
	})
}

// attemptFinished counts the result of the attempt of the worker and marks the worker idle.
func (w workerState) attemptFinished(res *attemptResult) {
	w.update(func(row *api.RunWorker) {
		row.Wait, row.Attempt = nil, nil
		c := &w.state.counts
		c.Attempts++
		if res.err != nil {
			c.Failures[res.category]++
		}
		if res.saved() {
			c.Accounts++
			if res.acct.Unconfirmed {
				c.Unconfirmed++
			}
		}
		c.BrowserBytesSent += res.rec.browserBytesSent
		c.BrowserBytesReceived += res.rec.browserBytesReceived
		c.HttpBytesSent += res.rec.httpBytesSent
		c.HttpBytesReceived += res.rec.httpBytesReceived
	})
}

// idle marks the worker as doing nothing.
func (w workerState) idle() {
	w.update(func(row *api.RunWorker) { row.Wait, row.Attempt = nil, nil })
}
