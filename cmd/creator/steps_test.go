package main

import (
	"bytes"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"jagex_account_creator/internal/logtest"
)

func TestStepTracker(t *testing.T) {
	tests := []struct {
		name       string
		before     time.Duration // the tracker starts this long before it is made
		run        func(st *stepTracker)
		wantSteps  []step
		wantTotal  time.Duration
		wantFailed string // "" when no step failed
	}{
		{
			name:      "a start before the tracker is made counts in the first step",
			before:    2 * time.Minute,
			run:       func(*stepTracker) { time.Sleep(time.Second) },
			wantSteps: []step{{"a", phaseBrowser, 2*time.Minute + time.Second}},
			wantTotal: 2*time.Minute + time.Second,
		},
		{
			name: "each step runs until the next one begins",
			run: func(st *stepTracker) {
				time.Sleep(time.Second)
				st.begin("b", phaseHTTP)
				time.Sleep(2 * time.Second)
				st.begin("c", phaseHTTP)
				time.Sleep(1500 * time.Millisecond)
			},
			wantSteps: []step{{"a", phaseBrowser, time.Second}, {"b", phaseHTTP, 2 * time.Second}, {"c", phaseHTTP, 1500 * time.Millisecond}},
			wantTotal: 4500 * time.Millisecond,
		},
		{
			name: "a step that begins at once has no duration",
			run: func(st *stepTracker) {
				st.begin("b", phaseBrowser)
				time.Sleep(time.Second)
			},
			wantSteps: []step{{"a", phaseBrowser, 0}, {"b", phaseBrowser, time.Second}},
			wantTotal: time.Second,
		},
		{
			name: "the open step at the error is the failed step",
			run: func(st *stepTracker) {
				time.Sleep(time.Second)
				st.begin("b", phaseHTTP)
				time.Sleep(time.Second)
				st.fail()
				st.begin("c", phaseBrowser)
				time.Sleep(time.Second)
				st.fail()
			},
			wantSteps:  []step{{"a", phaseBrowser, time.Second}, {"b", phaseHTTP, time.Second}, {"c", phaseBrowser, time.Second}},
			wantTotal:  3 * time.Second,
			wantFailed: "b",
		},
		{
			name: "the first step can fail",
			run: func(st *stepTracker) {
				st.fail()
				st.begin("b", phaseHTTP)
				st.fail()
			},
			wantSteps:  []step{{"a", phaseBrowser, 0}, {"b", phaseHTTP, 0}},
			wantFailed: "a",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				st := newStepTracker(time.Now().Add(-tt.before), "a", phaseBrowser)
				tt.run(st)
				total := st.finish()

				if total != tt.wantTotal {
					t.Errorf("total = %v, want %v", total, tt.wantTotal)
				}
				if !slices.Equal(st.steps, tt.wantSteps) {
					t.Errorf("steps = %v, want %v", st.steps, tt.wantSteps)
				}
				var sum time.Duration
				for _, s := range st.steps {
					sum += s.duration
				}
				if sum != total {
					t.Errorf("steps add up to %v, want the total %v", sum, total)
				}
				failed, ok := st.failedStep()
				if ok != (tt.wantFailed != "") || failed.name != tt.wantFailed {
					t.Errorf("failed step = %+v, %v, want %q", failed, ok, tt.wantFailed)
				}
			})
		})
	}
}

func TestStepTrackerOnBegin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		st := newStepTracker(start, "a", phaseBrowser)
		var got []string
		st.onBegin = func(name, phase string, at time.Time) {
			got = append(got, fmt.Sprintf("%s %s %v", name, phase, at.Sub(start)))
		}
		time.Sleep(time.Second)
		st.begin("b", phaseHTTP)
		time.Sleep(2 * time.Second)
		st.begin("c", phaseBrowser)

		if want := []string{"b http 1s", "c browser 3s"}; !slices.Equal(got, want) {
			t.Errorf("onBegin got %q, want %q", got, want)
		}
	})
}

func TestStepTrackerNilBegin(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("begin on a nil tracker panicked: %v", r)
		}
	}()
	var st *stepTracker
	st.begin("chrome_launch", phaseBrowser)
}

func TestStepTrackerGroup(t *testing.T) {
	st := &stepTracker{steps: []step{
		{name: "proxy_check", phase: phaseBrowser, duration: 1234567 * time.Microsecond},
		{name: "init_registration", phase: phaseHTTP, duration: 2 * time.Second},
	}}
	var buf bytes.Buffer
	logtest.New(&buf).LogAttrs(t.Context(), slog.LevelInfo, "m", st.group())
	if want := "msg=m steps.proxy_check=1.235s steps.init_registration=2s\n"; !strings.HasSuffix(buf.String(), want) {
		t.Errorf("log = %q, want the suffix %q", buf.String(), want)
	}
}

// stepNames returns the names of the steps of st, joined by spaces.
func stepNames(st *stepTracker) string {
	names := make([]string, len(st.steps))
	for i, s := range st.steps {
		names[i] = s.name
	}
	return strings.Join(names, " ")
}
