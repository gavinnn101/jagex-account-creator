package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"jagex_account_creator/internal/account"
	"jagex_account_creator/internal/api"
	"jagex_account_creator/internal/config"
)

// runStart is one start of a fake run: its config, its history and whether its context had ended at the start.
type runStart struct {
	cfg     *config.Config
	history runHistory
	ended   bool
}

// testRunFn is the type of apiServer.runFn.
type testRunFn = func(context.Context, <-chan struct{}, *slog.Logger, *config.Config, *runState, runHistory) error

// stagedRun returns a fake run that sends its start on starts and creates one
// worker row. It waits for the soft stage, then for the stop-at-once stage,
// then for a close of release, and returns err. The test closes release, so the
// run cannot end before the test reads the stop phases.
func stagedRun(starts chan<- runStart, release <-chan struct{}, err error) testRunFn {
	return func(ctx context.Context, stopNow <-chan struct{}, _ *slog.Logger, cfg *config.Config, state *runState, h runHistory) error {
		starts <- runStart{cfg: cfg, history: h, ended: ctx.Err() != nil}
		state.start(1)
		<-ctx.Done()
		<-stopNow
		<-release
		return err
	}
}

// newRunAPI returns an apiServer over a stored config with the IMAP password
// "stored" and a new store, whose runs call runFn.
func newRunAPI(t *testing.T, runFn testRunFn) *apiServer {
	t.Helper()
	s := newServeAPI(io.Discard)
	s.cfg, s.level, s.runFn, s.store = tempConfig(t, "stored"), &slog.LevelVar{}, runFn, newTestStore(t)
	return s
}

// runRequest sends one request to the API of s and decodes its run state. It
// fails the test on a status other than want.
func runRequest(ctx context.Context, t *testing.T, s *apiServer, method, path, body string, want int) api.RunState {
	t.Helper()
	var st api.RunState
	decodeBody(t, doAPI(ctx, s.handler(), method, path, body), want, &st)
	return st
}

// assertPhase fails the test when st is not in phase want.
func assertPhase(t *testing.T, st api.RunState, want api.RunStatePhase) {
	t.Helper()
	if st.Phase != want {
		t.Errorf("phase = %s, want %s", st.Phase, want)
	}
}

// postRefused posts body to POST /api/run and checks the 409 message.
func postRefused(t *testing.T, s *apiServer, body, want string) {
	t.Helper()
	var got api.Error
	decodeBody(t, doAPI(t.Context(), s.handler(), "POST", "/api/run", body), http.StatusConflict, &got)
	if got.Message != want {
		t.Errorf("409 message = %q, want %q", got.Message, want)
	}
}

func TestRunAPI(t *testing.T) {
	t.Run("the state before the first start is idle, and a stop changes nothing", func(t *testing.T) {
		s := newRunAPI(t, nil)
		const idle = `{"cooldowns":[],"counts":{"accounts":0,"attempts":0,"browser_bytes_received":0,"browser_bytes_sent":0,` +
			`"failures":{},"http_bytes_received":0,"http_bytes_sent":0,"unconfirmed":0},"phase":"idle","workers":[]}` + "\n"
		for _, req := range []struct{ method, path string }{{"GET", "/api/run"}, {"POST", "/api/run/stop"}, {"POST", "/api/run/stop-now"}} {
			w := doAPI(t.Context(), s.handler(), req.method, req.path, "")
			if w.Code != http.StatusOK || w.Body.String() != idle {
				t.Errorf("%s %s = %d %s, want 200 %s", req.method, req.path, w.Code, w.Body.String(), idle)
			}
		}
	})

	t.Run("a run goes through both stop stages and ends with its error", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			starts := make(chan runStart, 1)
			release := make(chan struct{})
			s := newRunAPI(t, stagedRun(starts, release, errors.New("run failed")))
			t0 := time.Now()
			body := postedConfig(t, s.cfg, func(c *api.Config) { c.Debug, c.Concurrency.AccountCount = true, 3 })
			// The run outlives the request, so the request context ends at once.
			ended, cancel := context.WithCancel(t.Context())
			cancel()

			st := runRequest(ended, t, s, "POST", "/api/run", body, http.StatusAccepted)
			got := <-starts

			assertPhase(t, st, api.RunPhaseRunning)
			if st.StartedAt == nil || !st.StartedAt.Equal(t0) || st.AccountCount == nil || *st.AccountCount != 3 {
				t.Errorf("start state = %+v, want started at the start with account_count 3", st)
			}
			if got.ended || !got.cfg.Debug || got.cfg.Email.IMAPPass != "stored" || got.cfg.Concurrency.AccountCount != 3 {
				t.Errorf("the run got the ended context %v and %+v, want a live context and the posted config with the stored password", got.ended, got.cfg)
			}
			// The run row is saved after the start answer.
			if got.history.accounts != s.store || got.history.runID != 1 || st.RunId != nil {
				t.Errorf("the run got the history %+v and the start answer the run id %v, want run 1 of the store of serve and no id", got.history, st.RunId)
			}
			if s.level.Level() != slog.LevelDebug {
				t.Errorf("level = %v, want DEBUG after a debug start", s.level.Level())
			}
			postRefused(t, s, body, "a run is in progress")

			synctest.Wait()
			st = runRequest(t.Context(), t, s, "GET", "/api/run", "", http.StatusOK)
			if len(st.Workers) != 1 || st.RunId == nil || *st.RunId != 1 {
				t.Errorf("workers = %+v, run id %v, want the one row of run 1", st.Workers, st.RunId)
			}
			assertPhase(t, runRequest(t.Context(), t, s, "POST", "/api/run/stop", "", http.StatusOK), api.RunPhaseStopping)
			assertPhase(t, runRequest(t.Context(), t, s, "POST", "/api/run/stop", "", http.StatusOK), api.RunPhaseStopping)
			postRefused(t, s, body, "a run is in progress")
			assertPhase(t, runRequest(t.Context(), t, s, "POST", "/api/run/stop-now", "", http.StatusOK), api.RunPhaseStoppingNow)

			close(release)
			if err := s.current.Wait(); err == nil || err.Error() != "run failed" {
				t.Errorf("Wait() = %v, want run failed", err)
			}
			st = runRequest(t.Context(), t, s, "POST", "/api/run/stop-now", "", http.StatusOK)
			assertPhase(t, st, api.RunPhaseEnded)
			if st.EndedAt == nil || st.Error == nil || *st.Error != "run failed" {
				t.Errorf("end state = %+v, want an end time and the run error", st)
			}
			if r := readRun(t, s.store, 1); r.Source != "serve" || r.EndedAt.IsZero() || r.Error != "run failed" {
				t.Errorf("run row = %+v, want an ended serve run with the run error", r)
			}

			// A start after the end runs again, and debug false sets the level back.
			runRequest(t.Context(), t, s, "POST", "/api/run", postedConfig(t, s.cfg, func(*api.Config) {}), http.StatusAccepted)
			if got := <-starts; got.history.runID != 2 {
				t.Errorf("the second run got the run id %d, want 2", got.history.runID)
			}
			if s.level.Level() != slog.LevelInfo {
				t.Errorf("level = %v, want INFO after a start with no debug", s.level.Level())
			}
			runRequest(t.Context(), t, s, "POST", "/api/run/stop-now", "", http.StatusOK)
			_ = s.current.Wait()
		})
	})

	t.Run("a run that fails before its first attempt ends with its error", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newRunAPI(t, func(context.Context, <-chan struct{}, *slog.Logger, *config.Config, *runState, runHistory) error {
				return errors.New("checking the mailbox: refused")
			})
			runRequest(t.Context(), t, s, "POST", "/api/run", postedConfig(t, s.cfg, func(*api.Config) {}), http.StatusAccepted)
			_ = s.current.Wait()

			st := runRequest(t.Context(), t, s, "GET", "/api/run", "", http.StatusOK)
			assertPhase(t, st, api.RunPhaseEnded)
			if st.Error == nil || *st.Error != "checking the mailbox: refused" || len(st.Workers) != 0 {
				t.Errorf("state = %+v, want the run error and no worker", st)
			}
		})
	})

	t.Run("an invalid config starts no run", func(t *testing.T) {
		s := newRunAPI(t, nil)
		body := postedConfig(t, s.cfg, func(c *api.Config) { c.Account.PasswordLength = 2 })
		var got api.ValidationResult
		decodeBody(t, doAPI(t.Context(), s.handler(), "POST", "/api/run", body), http.StatusUnprocessableEntity, &got)
		if got.Valid || len(got.Errors) != 1 || got.Errors[0] != "account.password_length must be at least 8, got 2" {
			t.Errorf("result = %+v, want the one password_length error", got)
		}
		assertPhase(t, runRequest(t.Context(), t, s, "GET", "/api/run", "", http.StatusOK), api.RunPhaseIdle)
	})

	t.Run("a value that does not parse starts no run", func(t *testing.T) {
		s := newRunAPI(t, nil)
		body := postedConfig(t, s.cfg, func(c *api.Config) { c.Chrome.Timeout = "soon" })
		var got api.Error
		decodeBody(t, doAPI(t.Context(), s.handler(), "POST", "/api/run", body), http.StatusBadRequest, &got)
		if !strings.HasPrefix(got.Message, `chrome.timeout: time: invalid duration "soon"`) {
			t.Errorf("message = %q, want the chrome.timeout error", got.Message)
		}
		assertPhase(t, runRequest(t.Context(), t, s, "GET", "/api/run", "", http.StatusOK), api.RunPhaseIdle)
	})

	t.Run("a start while serve is stopping is refused", func(t *testing.T) {
		s := newRunAPI(t, nil)
		s.drainRun(nil)
		postRefused(t, s, postedConfig(t, s.cfg, func(*api.Config) {}), "serve is stopping")
	})

	t.Run("the drain stops the run and waits for its end", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newRunAPI(t, func(ctx context.Context, _ <-chan struct{}, _ *slog.Logger, _ *config.Config, _ *runState, _ runHistory) error {
				<-ctx.Done()
				return nil
			})
			runRequest(t.Context(), t, s, "POST", "/api/run", postedConfig(t, s.cfg, func(*api.Config) {}), http.StatusAccepted)

			s.drainRun(nil)

			if !ended(s.current) {
				t.Error("drainRun returned before the end of the run")
			}
			assertPhase(t, s.state.snapshot(), api.RunPhaseEnded)
		})
	})
}

// The run logger of each serve run writes each line to the log file of that run
// and publishes it on the log hub of serve.
func TestRunAPIRunLog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newRunAPI(t, func(_ context.Context, _ <-chan struct{}, logger *slog.Logger, _ *config.Config, _ *runState, h runHistory) error {
			logger.Info("checking the mailbox", "run", h.runID)
			return errors.New("checking the mailbox: refused")
		})
		lines, cancel := s.logs.subscribe()
		defer cancel()

		for range 2 {
			runRequest(t.Context(), t, s, "POST", "/api/run", postedConfig(t, s.cfg, func(*api.Config) {}), http.StatusAccepted)
			_ = s.current.Wait()
		}

		got, _ := published(lines)
		want := append(fileLines(t, s, 1), fileLines(t, s, 2)...)
		if !reflect.DeepEqual(got, want) || len(want) != 6 {
			t.Fatalf("published %+v, want the three lines of each run file %+v", got, want)
		}
		for i, msg := range []string{"run started", "checking the mailbox", "run ended"} {
			if got[i].Msg != msg || got[i+3].Msg != msg || got[i].RunId != 1 || got[i+3].RunId != 2 {
				t.Errorf("lines %d and %d = %+v and %+v, want %q of run 1, then of run 2", i, i+3, got[i], got[i+3], msg)
			}
		}
		if r := readRun(t, s.store, 2); r.Error != "checking the mailbox: refused" {
			t.Errorf("run row 2 = %+v, want the run error", r)
		}
	})
}

// Serve shows the history of its own database only, so a run of another database does not start.
func TestStartRunOtherDatabase(t *testing.T) {
	s := newRunAPI(t, nil)
	body := postedConfig(t, s.cfg, func(c *api.Config) { c.Database = "other.db" })
	var got api.ValidationResult
	decodeBody(t, doAPI(t.Context(), s.handler(), "POST", "/api/run", body), http.StatusUnprocessableEntity, &got)
	if want := fmt.Sprintf("database must be the database of serve, %q, got \"other.db\"", s.cfg.Database); got.Valid || len(got.Errors) != 1 || got.Errors[0] != want {
		t.Errorf("result = %+v, want the one error %q", got, want)
	}
	assertPhase(t, runRequest(t.Context(), t, s, "GET", "/api/run", "", http.StatusOK), api.RunPhaseIdle)
}

// A start with no domains refuses a mailbox that already has an account, before a run row exists.
func TestStartRunMailboxUsed(t *testing.T) {
	s := newRunAPI(t, nil)
	if err := s.store.WriteAccount(t.Context(), &account.Account{Email: "used@outlook.com", Username: "u", Password: "p", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	noDomain := func(user string) string {
		return postedConfig(t, s.cfg, func(c *api.Config) {
			c.Email.ImapDomains, c.Email.ImapUser, c.Concurrency.AccountCount = []string{}, user, 1
		})
	}
	var got api.ValidationResult
	decodeBody(t, doAPI(t.Context(), s.handler(), "POST", "/api/run", noDomain("used@outlook.com")), http.StatusUnprocessableEntity, &got)
	if want := "too few mailboxes with no account: each of the 1 mailbox already has an account. Add mailboxes with no account"; got.Valid || len(got.Errors) != 1 || got.Errors[0] != want {
		t.Errorf("result = %+v, want the one mailbox error", got)
	}
	assertPhase(t, runRequest(t.Context(), t, s, "GET", "/api/run", "", http.StatusOK), api.RunPhaseIdle)
	if runs, _, err := s.store.Runs(t.Context(), 0, 10); err != nil || len(runs) != 0 {
		t.Errorf("runs = %+v, %v, want none", runs, err)
	}

	t.Run("a store that fails the check answers 500", func(t *testing.T) {
		if err := s.store.Close(); err != nil {
			t.Fatal(err)
		}
		w := doAPI(t.Context(), s.handler(), "POST", "/api/run", noDomain("new@outlook.com"))
		if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "checking the mailbox new@outlook.com for an account: ") {
			t.Errorf("answer = %d %s, want 500 with the check error", w.Code, w.Body.String())
		}
	})
}

// A subscriber of the run state never misses the start of a run.
func TestSubscribeRun(t *testing.T) {
	t.Run("a subscriber before the first start gets the idle state, then the first state of the run, its run id and its rows", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			starts := make(chan runStart, 1)
			release := make(chan struct{})
			s := newRunAPI(t, stagedRun(starts, release, nil))
			first, ch, cancel := s.subscribeRun()
			defer cancel()
			assertPhase(t, first, api.RunPhaseIdle)

			runRequest(t.Context(), t, s, "POST", "/api/run", postedConfig(t, s.cfg, func(*api.Config) {}), http.StatusAccepted)
			<-starts
			synctest.Wait()

			got, _ := published(ch)
			if len(got) != 3 || got[0].Phase != api.RunPhaseRunning || got[0].RunId != nil || len(got[0].Workers) != 0 {
				t.Fatalf("published %+v, want the first state of the run with no run id and no row, then two more", got)
			}
			if got[1].RunId == nil || *got[1].RunId != 1 || len(got[1].Workers) != 0 || got[2].RunId == nil || len(got[2].Workers) != 1 {
				t.Errorf("published %+v, want the run id 1 with no row, then its one row", got[1:])
			}
			runRequest(t.Context(), t, s, "POST", "/api/run/stop-now", "", http.StatusOK)
			close(release)
			_ = s.current.Wait()
		})
	})

	t.Run("a subscriber during a run gets its state, then each later mark", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			starts := make(chan runStart, 1)
			release := make(chan struct{})
			s := newRunAPI(t, stagedRun(starts, release, nil))
			runRequest(t.Context(), t, s, "POST", "/api/run", postedConfig(t, s.cfg, func(*api.Config) {}), http.StatusAccepted)
			<-starts
			synctest.Wait()

			first, ch, cancel := s.subscribeRun()
			defer cancel()
			runRequest(t.Context(), t, s, "POST", "/api/run/stop", "", http.StatusOK)

			if len(first.Workers) != 1 {
				t.Errorf("first = %+v, want the state with the one row of the run", first)
			}
			if got, _ := published(ch); len(got) != 1 || got[0].Phase != api.RunPhaseStopping {
				t.Errorf("published %+v, want the one state of the stop", got)
			}
			runRequest(t.Context(), t, s, "POST", "/api/run/stop-now", "", http.StatusOK)
			close(release)
			_ = s.current.Wait()
		})
	})
}

// A real server keeps serving through the first stop stage, so a client sees the stop.
func TestServeAPIDrainsRun(t *testing.T) {
	ln := listenLoopback(t)
	softEnded := make(chan struct{})
	nowClosed := make(chan struct{})
	s := newRunAPI(t, func(ctx context.Context, stopNow <-chan struct{}, _ *slog.Logger, _ *config.Config, _ *runState, _ runHistory) error {
		select {
		case <-ctx.Done():
			close(softEnded)
		case <-time.After(5 * time.Second):
			return errors.New("no soft stage")
		}
		select {
		case <-stopNow:
			close(nowClosed)
		case <-time.After(5 * time.Second):
			return errors.New("no stop-at-once stage")
		}
		return nil
	})
	c := startRun(t.Context(), func(ctx context.Context, stopNow <-chan struct{}) error {
		return serveAPI(ctx, stopNow, s, ln, guardAPI(s.handler()))
	})
	client := newTestClient(t)
	base := "http://" + ln.Addr().String() + "/api/run"
	body := postedConfig(t, s.cfg, func(*api.Config) {})

	resp, err := client.Post(base, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start status = %d, want 202", resp.StatusCode)
	}

	c.Stop()
	select {
	case <-softEnded:
	case <-time.After(5 * time.Second):
		t.Fatal("the stop of serve did not reach the run")
	}
	resp, err = client.Get(base)
	if err != nil {
		t.Fatal(err)
	}
	var st api.RunState
	err = json.NewDecoder(resp.Body).Decode(&st)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	assertPhase(t, st, api.RunPhaseStopping)
	resp, err = client.Post(base, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("start during the stop = %d, want 409", resp.StatusCode)
	}

	c.StopNow()
	if err := waitRun(t, c); err != nil {
		t.Errorf("serveAPI() = %v, want nil", err)
	}
	if !isClosed(nowClosed) {
		t.Error("serve returned before the run saw the stop-at-once stage")
	}
	assertPhase(t, s.state.snapshot(), api.RunPhaseEnded)
	assertClosed(t, ln)
}
