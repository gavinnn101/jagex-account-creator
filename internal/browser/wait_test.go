package browser

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/mafredri/cdp"
	"github.com/mafredri/cdp/protocol/runtime"

	"jagex_account_creator/internal/browser/cdptest"
	"jagex_account_creator/internal/logtest"
)

const testChallengeTitle = "Security | Example"

// pageRuntime returns a fake Runtime that reads title as document.title and
// "state" as the page state.
func pageRuntime(title string) *cdptest.Runtime {
	return &cdptest.Runtime{Answer: func(expression string) (*runtime.EvaluateReply, error) {
		if expression == "document.title" {
			return cdptest.Value(`"` + title + `"`), nil
		}
		return cdptest.Value(`"state"`), nil
	}}
}

func TestClickIfChallenge(t *testing.T) {
	tests := []struct {
		name       string
		runtime    *cdptest.Runtime
		dom        *cdptest.DOM
		wantClicks int
		wantLog    string
		logPrefix  bool // the click lines after wantLog carry jittered coordinates
	}{
		{name: "not a challenge", runtime: pageRuntime("Account"), dom: clickableDOM()},
		{
			name:       "the site's challenge title",
			runtime:    pageRuntime(testChallengeTitle),
			dom:        clickableDOM(),
			wantClicks: 1,
			wantLog:    `level=DEBUG msg="challenge detected during wait, clicking" waiting_for=clearance` + "\n",
			logPrefix:  true,
		},
		{
			name:    "an unreadable title and no checkbox",
			runtime: staticRuntime(nil, errFake),
			dom:     &cdptest.DOM{DocumentErr: errFake},
			wantLog: `level=DEBUG msg="challenge detected during wait, clicking" waiting_for=clearance` + "\n" +
				`level=DEBUG msg="Turnstile click failed" waiting_for=clearance error="DOM.getDocument: fake failure"` + "\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := &cdptest.Input{}
			var buf bytes.Buffer
			g := newClearanceGate(&cdp.Client{Runtime: tt.runtime, DOM: tt.dom, Input: in}, logtest.New(&buf), testChallengeTitle, func() {})

			g.clickIfChallenge(t.Context())

			if got := len(in.Events) / 3; got != tt.wantClicks {
				t.Errorf("clicks = %d, want %d", got, tt.wantClicks)
			}
			if log := buf.String(); log != tt.wantLog && (!tt.logPrefix || !strings.HasPrefix(log, tt.wantLog)) {
				t.Errorf("log = %q, want %q (prefix only: %v)", log, tt.wantLog, tt.logPrefix)
			}
		})
	}
}

// waitResult is what one Wait call did.
type waitResult struct {
	err     error
	elapsed time.Duration
	titles  int // document.title reads, one for each click tick
	states  int // page state reads
	log     string
}

// runWait runs Wait on a page that is no challenge. done and cancel close the
// gate's done channel and cancel the parent at their offsets. A zero offset
// means never. Call it in a synctest bubble.
func runWait(ctx context.Context, t *testing.T, timeout, done, cancel time.Duration, debug bool) waitResult {
	t.Helper()
	rt := pageRuntime("Account")
	c := &cdp.Client{Runtime: rt, Page: &cdptest.Page{Screenshot: []byte("png")}}
	var buf bytes.Buffer
	g := newClearanceGate(c, logtest.New(&buf), testChallengeTitle, func() {})
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	if done > 0 {
		time.AfterFunc(done, func() { close(g.done) })
	}
	if cancel > 0 {
		time.AfterFunc(cancel, stop)
	}

	start := time.Now()
	err := g.Wait(ctx, timeout, debug)

	r := waitResult{err: err, elapsed: time.Since(start), log: buf.String()}
	for _, call := range rt.Calls {
		if call.Expression == "document.title" {
			r.titles++
		} else {
			r.states++
		}
	}
	return r
}

func TestWait(t *testing.T) {
	tests := []struct {
		name        string
		timeout     time.Duration
		done        time.Duration
		cancel      time.Duration
		wantErr     error
		wantElapsed time.Duration
		wantTitles  int
	}{
		{name: "both sources seen", timeout: 5 * time.Second, done: 2500 * time.Millisecond, wantElapsed: 2500 * time.Millisecond, wantTitles: 2},
		{name: "parent canceled", timeout: 5 * time.Second, cancel: 500 * time.Millisecond, wantErr: context.Canceled, wantElapsed: 500 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := runWait(t.Context(), t, tt.timeout, tt.done, tt.cancel, false)

				if !errors.Is(r.err, tt.wantErr) || (tt.wantErr == nil) != (r.err == nil) {
					t.Errorf("Wait() error = %v, want %v", r.err, tt.wantErr)
				}
				if r.elapsed != tt.wantElapsed || r.titles != tt.wantTitles || r.states != 0 {
					t.Errorf("Wait() took %v with %d title and %d state reads, want %v with %d and 0",
						r.elapsed, r.titles, r.states, tt.wantElapsed, tt.wantTitles)
				}
				if strings.Contains(r.log, "level=WARN") {
					t.Errorf("log = %q, want no warning", r.log)
				}
			})
		})
	}
}

func TestWaitParentDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 700*time.Millisecond)
		defer cancel()

		r := runWait(ctx, t, 5*time.Second, 0, 0, false)

		if !errors.Is(r.err, context.DeadlineExceeded) || r.elapsed != 700*time.Millisecond || r.states != 0 {
			t.Errorf("Wait() = %v after %v with %d state reads, want %v after 700ms with none",
				r.err, r.elapsed, r.states, context.DeadlineExceeded)
		}
	})
}

// TestWaitTimeout lets one diagnostic tick pass before the gate's timeout. At
// 10 s the click tick and the diagnostic tick are ready together, so the test
// checks counts, never the order of the lines.
func TestWaitTimeout(t *testing.T) {
	for _, debug := range []bool{false, true} {
		t.Run(map[bool]string{false: "debug off", true: "debug on"}[debug], func(t *testing.T) {
			// The debug capture goes under the working directory.
			t.Chdir(t.TempDir())
			var log string
			synctest.Test(t, func(t *testing.T) {
				r := runWait(t.Context(), t, 10500*time.Millisecond, 0, 0, debug)
				log = r.log

				if !errors.Is(r.err, ErrChallengeTimeout) || r.elapsed != 10500*time.Millisecond {
					t.Errorf("Wait() = %v after %v, want %v after 10.5s", r.err, r.elapsed, ErrChallengeTimeout)
				}
				if r.titles != 10 || r.states != 2 {
					t.Errorf("reads = %d titles and %d states, want 10 and 2", r.titles, r.states)
				}
				for _, line := range []string{
					`level=DEBUG msg="page state" label="clearance wait tick 1" data=state`,
					`level=DEBUG msg="page state" label="clearance wait timeout" data=state`,
				} {
					if n := strings.Count(r.log, line); n != 1 {
						t.Errorf("log has %d lines %q, want 1:\n%s", n, line, r.log)
					}
				}
			})

			files, err := filepath.Glob(filepath.Join(debugScreenshotDir, "clearance_wait_1_*.png"))
			if err != nil {
				t.Fatal(err)
			}
			if !debug {
				if len(files) != 0 {
					t.Errorf("captures = %q, want none with debug off", files)
				}
				return
			}
			if len(files) != 1 {
				t.Fatalf("captures = %q, want one", files)
			}
			if data, err := os.ReadFile(files[0]); err != nil || string(data) != "png" {
				t.Errorf("%s = %q, %v, want the capture", files[0], data, err)
			}
			if line := `level=DEBUG msg="screenshot saved" file=` + files[0] + "\n"; strings.Count(log, line) != 1 {
				t.Errorf("log has no line %q:\n%s", line, log)
			}
		})
	}
}
