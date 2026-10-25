package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"jagex_account_creator/internal/account"
	"jagex_account_creator/internal/api"
	"jagex_account_creator/internal/proxy"
)

// jsonOf returns the JSON of v, as the API sends it. It drops the monotonic clock reading of a time.
func jsonOf(t *testing.T, v any) string {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// testRunState returns a run state for accountCount accounts on its own events hub.
func testRunState(accountCount int) *runState {
	return newRunState(accountCount, newHub[api.RunState](runEventBuffer))
}

// published returns the values that ch holds now, and whether ch is closed. It does not wait.
func published[T any](ch <-chan T) (got []T, closed bool) {
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				return got, true
			}
			got = append(got, v)
		default:
			return got, false
		}
	}
}

// resultOf returns the result of an attempt that moved the four byte counts n,
// 2n, 3n and 4n.
func resultOf(n int64, acct *account.Account, err error, category string) *attemptResult {
	rec := &attemptRecord{browserBytesSent: n, browserBytesReceived: 2 * n, httpBytesSent: 3 * n, httpBytesReceived: 4 * n}
	return &attemptResult{rec: rec, acct: acct, err: err, category: category}
}

// Each case runs on the fake clock from t0, on a run of 5 accounts with two workers.
// ws is the handle of worker 1, so each row of worker 0 stays idle.
func TestRunState(t *testing.T) {
	failed := errors.New("failed")
	tests := []struct {
		name   string
		events int // the states that the marks of run publish
		run    func(s *runState, ws workerState)
		want   func(t0 time.Time, st *api.RunState) // edits the state of the run with two idle workers
	}{
		{
			name:   "start creates an idle row for each worker",
			events: 0,
			run:    func(*runState, workerState) {},
			want:   func(time.Time, *api.RunState) {},
		},
		{
			name:   "the run id of the run row",
			events: 1,
			run:    func(s *runState, _ workerState) { s.runRecorded(7) },
			want:   func(_ time.Time, st *api.RunState) { st.RunId = new(int64(7)) },
		},
		{
			name:   "a proxy wait",
			events: 1,
			run: func(_ *runState, ws workerState) {
				time.Sleep(time.Second)
				ws.waitProxy()
			},
			want: func(t0 time.Time, st *api.RunState) {
				st.Workers[1].Wait = &api.RunWait{Kind: api.RunWaitProxy, Since: t0.Add(time.Second)}
			},
		},
		{
			name:   "an attempt start replaces the wait, and the slot wait is its open step",
			events: 2,
			run: func(_ *runState, ws workerState) {
				ws.waitProxy()
				time.Sleep(time.Second)
				ws.attemptStarted(testProxy(), time.Now())
			},
			want: func(t0 time.Time, st *api.RunState) {
				at := t0.Add(time.Second)
				st.Workers[1].Attempt = &api.RunAttempt{StartedAt: at, Proxy: new(testProxyAddr), Step: stepSlotWait, StepPhase: phaseBrowser, StepStartedAt: at}
			},
		},
		{
			name:   "an attempt over a direct connection has no proxy",
			events: 1,
			run:    func(_ *runState, ws workerState) { ws.attemptStarted(proxy.Proxy{}, time.Now()) },
			want: func(t0 time.Time, st *api.RunState) {
				st.Workers[1].Attempt = &api.RunAttempt{StartedAt: t0, Step: stepSlotWait, StepPhase: phaseBrowser, StepStartedAt: t0}
			},
		},
		{
			name:   "a step start keeps the attempt start",
			events: 2,
			run: func(_ *runState, ws workerState) {
				ws.attemptStarted(testProxy(), time.Now())
				time.Sleep(2 * time.Second)
				ws.stepStarted("submit_code", phaseHTTP, time.Now())
			},
			want: func(t0 time.Time, st *api.RunState) {
				st.Workers[1].Attempt = &api.RunAttempt{StartedAt: t0, Proxy: new(testProxyAddr), Step: "submit_code", StepPhase: phaseHTTP, StepStartedAt: t0.Add(2 * time.Second)}
			},
		},
		{
			name:   "a backoff replaces the attempt",
			events: 2,
			run: func(_ *runState, ws workerState) {
				ws.attemptStarted(testProxy(), time.Now())
				time.Sleep(time.Second)
				ws.backoff(5 * time.Second)
			},
			want: func(t0 time.Time, st *api.RunState) {
				st.Workers[1].Wait = &api.RunWait{Kind: api.RunWaitBackoff, Since: t0.Add(time.Second), Until: new(t0.Add(6 * time.Second))}
			},
		},
		{
			name:   "idle clears the row",
			events: 2,
			run: func(_ *runState, ws workerState) {
				ws.attemptStarted(testProxy(), time.Now())
				ws.idle()
			},
			want: func(time.Time, *api.RunState) {},
		},
		{
			name:   "each attempt end counts its result and idles the worker",
			events: 6,
			run: func(_ *runState, ws workerState) {
				ws.attemptStarted(testProxy(), time.Now())
				ws.attemptFinished(resultOf(1, &account.Account{}, nil, ""))
				ws.attemptFinished(resultOf(10, nil, failed, "challenge_timeout"))
				ws.attemptFinished(resultOf(100, nil, failed, "challenge_timeout"))
				ws.attemptFinished(resultOf(1000, &account.Account{Unconfirmed: true}, failed, "cancelled"))
				ws.attemptFinished(resultOf(10000, &account.Account{Unconfirmed: true}, fmt.Errorf("%w: disk full", errSaveFailed), "save_failed"))
			},
			want: func(_ time.Time, st *api.RunState) {
				st.Counts = api.RunCounts{
					Attempts: 5, Accounts: 2, Unconfirmed: 1,
					Failures:         map[string]int{"challenge_timeout": 2, "cancelled": 1, "save_failed": 1},
					BrowserBytesSent: 11111, BrowserBytesReceived: 22222, HttpBytesSent: 33333, HttpBytesReceived: 44444,
				}
			},
		},
		{
			name:   "a cooldown one tick before its end still runs",
			events: 1,
			run: func(s *runState, _ workerState) {
				s.proxyCooled(testProxyAddr, "rate limited", time.Now().Add(5*time.Minute), 2)
				time.Sleep(5*time.Minute - time.Nanosecond)
			},
			want: func(t0 time.Time, st *api.RunState) {
				st.Cooldowns = []api.RunCooldown{{Proxy: testProxyAddr, Reason: "rate limited", Until: t0.Add(5 * time.Minute), Failures: 2}}
			},
		},
		{
			name:   "a cooldown drops at its end",
			events: 1,
			run: func(s *runState, _ workerState) {
				s.proxyCooled(testProxyAddr, "rate limited", time.Now().Add(5*time.Minute), 2)
				time.Sleep(5 * time.Minute)
			},
			want: func(time.Time, *api.RunState) {},
		},
		{
			name:   "the first stop stage",
			events: 1,
			run: func(s *runState, _ workerState) {
				s.stop(false)
				s.stop(false)
			},
			want: func(_ time.Time, st *api.RunState) { st.Phase = api.RunPhaseStopping },
		},
		{
			name:   "the second stop stage after the first",
			events: 2,
			run: func(s *runState, _ workerState) {
				s.stop(false)
				s.stop(true)
				s.stop(false)
			},
			want: func(_ time.Time, st *api.RunState) { st.Phase = api.RunPhaseStoppingNow },
		},
		{
			name:   "the second stop stage at once",
			events: 1,
			run:    func(s *runState, _ workerState) { s.stop(true) },
			want:   func(_ time.Time, st *api.RunState) { st.Phase = api.RunPhaseStoppingNow },
		},
		{
			name:   "the end with no error, and a stop after it",
			events: 1,
			run: func(s *runState, _ workerState) {
				time.Sleep(3 * time.Second)
				s.end(nil)
				s.stop(true)
				s.stop(false)
			},
			want: func(t0 time.Time, st *api.RunState) {
				st.Phase, st.EndedAt = api.RunPhaseEnded, new(t0.Add(3*time.Second))
			},
		},
		{
			name:   "the end with an error",
			events: 1,
			run:    func(s *runState, _ workerState) { s.end(errors.New("checking the mailbox: refused")) },
			want: func(t0 time.Time, st *api.RunState) {
				st.Phase, st.EndedAt, st.Error = api.RunPhaseEnded, new(t0), new("checking the mailbox: refused")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				t0 := time.Now()
				s := testRunState(5)
				s.start(2)
				_, ch, cancel := s.subscribe()
				defer cancel()

				tt.run(s, workerState{state: s, worker: 1})

				if got, _ := published(ch); len(got) != tt.events {
					t.Errorf("the marks published %d states, want %d", len(got), tt.events)
				}
				want := api.RunState{
					Phase:        api.RunPhaseRunning,
					StartedAt:    new(t0),
					AccountCount: new(5),
					Workers:      []api.RunWorker{{Worker: 0}, {Worker: 1}},
					Cooldowns:    []api.RunCooldown{},
					Counts:       api.RunCounts{Failures: map[string]int{}},
				}
				tt.want(t0, &want)
				if got, want := jsonOf(t, s.snapshot()), jsonOf(t, want); got != want {
					t.Errorf("snapshot =\n%s\nwant\n%s", got, want)
				}
			})
		})
	}
}

// Each mark publishes one state, and that state is the snapshot right after the mark.
func TestRunStatePublishesEachMark(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		events := newHub[api.RunState](runEventBuffer)
		ch, cancel := events.subscribe()
		defer cancel()
		var s *runState
		ws := func() workerState { return workerState{state: s, worker: 1} }
		marks := []struct {
			name string
			mark func()
		}{
			{"newRunState", func() { s = newRunState(5, events) }},
			{"start", func() { s.start(2) }},
			{"runRecorded", func() { s.runRecorded(1) }},
			{"waitProxy", func() { ws().waitProxy() }},
			{"attemptStarted", func() { ws().attemptStarted(testProxy(), time.Now()) }},
			{"stepStarted", func() { ws().stepStarted("navigate", phaseBrowser, time.Now()) }},
			{"attemptFinished", func() { ws().attemptFinished(resultOf(1, &account.Account{}, nil, "")) }},
			{"backoff", func() { ws().backoff(time.Second) }},
			{"idle", func() { ws().idle() }},
			{"proxyCooled", func() { s.proxyCooled(testProxyAddr, "rate limited", time.Now().Add(time.Minute), 1) }},
			{"stop", func() { s.stop(false) }},
			{"stop now", func() { s.stop(true) }},
			{"end", func() { s.end(nil) }},
		}
		for _, m := range marks {
			time.Sleep(time.Second)
			m.mark()
			got, _ := published(ch)
			if len(got) != 1 {
				t.Errorf("%s published %d states, want 1", m.name, len(got))
				continue
			}
			if got, want := jsonOf(t, got[0]), jsonOf(t, s.snapshot()); got != want {
				t.Errorf("%s published\n%s\nwant the snapshot\n%s", m.name, got, want)
			}
		}
	})
}

// A subscriber that never reads does not hold a mark. The hub closes it when its buffer is full.
func TestRunStateMarksDoNotWaitForAReader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testRunState(1)
		s.start(1)
		_, ch, cancel := s.subscribe()
		defer cancel()
		ws := workerState{state: s}

		for range runEventBuffer + 1 {
			ws.idle()
		}

		if got, closed := published(ch); len(got) != runEventBuffer || !closed {
			t.Errorf("the subscriber got %d states, closed = %v, want %d and closed", len(got), closed, runEventBuffer)
		}
	})
}

func TestRunStateSnapshotIsACopy(t *testing.T) {
	s := testRunState(1)
	s.start(1)
	_, ch, cancel := s.subscribe()
	defer cancel()
	ws := workerState{state: s}
	ws.attemptStarted(testProxy(), time.Now())
	ws.attemptFinished(resultOf(1, nil, errors.New("failed"), "unknown"))
	before := s.snapshot()
	events, _ := published(ch)
	if len(events) != 2 {
		t.Fatalf("the marks published %d states, want 2", len(events))
	}
	want, wantEvent := jsonOf(t, before), jsonOf(t, events[1])

	ws.attemptStarted(testProxy(), time.Now())
	ws.stepStarted("navigate", phaseBrowser, time.Now())
	ws.attemptFinished(resultOf(1, nil, errors.New("failed"), "unknown"))

	if got := jsonOf(t, before); got != want {
		t.Errorf("a later mark changed a snapshot:\n%s\nwant\n%s", got, want)
	}
	if got := jsonOf(t, events[1]); got != wantEvent {
		t.Errorf("a later mark changed a published state:\n%s\nwant\n%s", got, wantEvent)
	}
}

// A subscriber gets the snapshot of the moment it subscribed, then each later mark.
func TestRunStateSubscribe(t *testing.T) {
	s := testRunState(1)
	s.start(1)
	first, ch, cancel := s.subscribe()
	ws := workerState{state: s}
	ws.waitProxy()

	if got, want := jsonOf(t, first), jsonOf(t, api.RunState{
		Phase: api.RunPhaseRunning, StartedAt: s.snapshot().StartedAt, AccountCount: new(1),
		Workers: []api.RunWorker{{}}, Cooldowns: []api.RunCooldown{}, Counts: api.RunCounts{Failures: map[string]int{}},
	}); got != want {
		t.Errorf("first =\n%s\nwant\n%s", got, want)
	}
	if got, _ := published(ch); len(got) != 1 || got[0].Workers[0].Wait == nil {
		t.Errorf("published %+v, want the one state of the proxy wait", got)
	}
	cancel()
	if _, closed := published(ch); !closed {
		t.Error("cancel left the subscription open")
	}
}

func TestRunStateProxyCooledDropsEndedCooldowns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testRunState(1)
		s.proxyCooled(testProxyAddr, "rate limited", time.Now().Add(time.Minute), 1)
		time.Sleep(time.Minute)
		s.proxyCooled(otherProxyAddr, "page load", time.Now().Add(time.Minute), 1)

		if len(s.cooldowns) != 1 || s.cooldowns[0].Proxy != otherProxyAddr {
			t.Errorf("cooldowns = %+v, want only the running cooldown of %s", s.cooldowns, otherProxyAddr)
		}
	})
}

// The CLI passes a nil state, so each call that the run makes on it does nothing.
func TestRunStateNil(t *testing.T) {
	var s *runState
	ws := workerState{worker: 3}

	s.start(2)
	s.runRecorded(1)
	s.proxyCooled(testProxyAddr, "rate limited", time.Now(), 1)
	ws.waitProxy()
	ws.attemptStarted(testProxy(), time.Now())
	ws.stepStarted("navigate", phaseBrowser, time.Now())
	ws.backoff(time.Second)
	ws.attemptFinished(resultOf(1, nil, nil, ""))
	ws.idle()

	const want = `{"cooldowns":[],"counts":{"accounts":0,"attempts":0,"browser_bytes_received":0,"browser_bytes_sent":0,` +
		`"failures":{},"http_bytes_received":0,"http_bytes_sent":0,"unconfirmed":0},"phase":"idle","workers":[]}`
	if got := jsonOf(t, s.snapshot()); got != want {
		t.Errorf("snapshot of a nil state =\n%s\nwant\n%s", got, want)
	}
}

// A run that ends before its workers start has no worker row, and its JSON holds no null.
func TestRunStateEndBeforeStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t0 := time.Now()
		s := testRunState(-1)
		s.end(context.Canceled)

		at, err := json.Marshal(t0)
		if err != nil {
			t.Fatal(err)
		}
		want := `{"account_count":-1,"cooldowns":[],"counts":{"accounts":0,"attempts":0,"browser_bytes_received":0,"browser_bytes_sent":0,` +
			`"failures":{},"http_bytes_received":0,"http_bytes_sent":0,"unconfirmed":0},"ended_at":` + string(at) +
			`,"error":"context canceled","phase":"ended","started_at":` + string(at) + `,"workers":[]}`
		if got := jsonOf(t, s.snapshot()); got != want {
			t.Errorf("snapshot =\n%s\nwant\n%s", got, want)
		}
	})
}
