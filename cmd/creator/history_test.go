package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"jagex_account_creator/internal/account"
	"jagex_account_creator/internal/api"
	"jagex_account_creator/internal/config"
	"jagex_account_creator/internal/logtest"
	"jagex_account_creator/internal/store"
)

// newTestHistory saves a new run row to s and returns its history, so an attempt row has a run to refer to.
func newTestHistory(t *testing.T, s *store.Store) runHistory {
	t.Helper()
	id, err := s.WriteRun(t.Context(), string(api.RunSourceCLI), time.Now(), json.RawMessage("{}"))
	if err != nil {
		t.Fatalf("WriteRun: %v", err)
	}
	return runHistory{accounts: s, runID: id}
}

// readRun returns the run id of s.
func readRun(t *testing.T, s *store.Store, id int64) store.Run {
	t.Helper()
	r, err := s.Run(t.Context(), id)
	if err != nil {
		t.Fatalf("Run(%d): %v", id, err)
	}
	return r
}

// tempConfig returns the stored config of storedConfig with its database in a
// new temp dir, so the log file of a run lands there.
func tempConfig(t *testing.T, pass string) *config.Config {
	t.Helper()
	cfg := storedConfig(t, pass)
	cfg.Database = filepath.Join(t.TempDir(), "creator.db")
	return cfg
}

// readRunLog returns the log file of run runID beside the database of cfg.
func readRunLog(t *testing.T, cfg *config.Config, runID int64) string {
	t.Helper()
	data, err := os.ReadFile(runLogPath(cfg.Database, runID))
	if err != nil {
		t.Fatalf("reading the run log: %v", err)
	}
	return string(data)
}

func TestRecordRun(t *testing.T) {
	failed := errors.New("run failed")
	tests := []struct {
		name    string
		workErr error
		cancel  bool // the work ends the run context before it returns
		stopped bool // the run context has ended before the start
		wantErr string
		wantEnd string // the end line of the log file
	}{
		{name: "a run with no error", wantEnd: `level=INFO msg="run ended" run_id=1`},
		{name: "a run with an error", workErr: failed, wantErr: "run failed", wantEnd: `level=INFO msg="run ended" run_id=1 error="run failed"`},
		{name: "a stop before the end still saves the end", cancel: true, wantEnd: `level=INFO msg="run ended" run_id=1`},
		{name: "a stop before the start still saves the run", stopped: true, wantEnd: `level=INFO msg="run ended" run_id=1`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := newTestStore(t)
				cfg := tempConfig(t, "stored-secret")
				t0 := time.Now()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if tt.stopped {
					cancel()
				}
				var got runHistory
				var base bytes.Buffer

				err := recordRun(ctx, s, api.RunSourceServe, cfg, runLogOptions{base: logtest.New(&base).Handler(), level: slog.LevelInfo}, func(_ context.Context, logger *slog.Logger, h runHistory) error {
					got = h
					logger.Debug("hidden")
					logger.Info("working", "n", 1)
					time.Sleep(time.Minute)
					if tt.cancel {
						cancel()
					}
					return tt.workErr
				})

				if gotErr := errText(err); gotErr != tt.wantErr {
					t.Errorf("recordRun() = %q, want %q", gotErr, tt.wantErr)
				}
				if got.accounts != s || got.runID != 1 {
					t.Fatalf("the work got %+v, want the store and run 1", got)
				}
				r := readRun(t, s, 1)
				want := store.Run{ID: 1, Source: "serve", StartedAt: t0.UTC(), EndedAt: t0.Add(time.Minute).UTC(), Error: tt.wantErr, Config: json.RawMessage(jsonOf(t, apiConfig(cfg)))}
				if g, w := jsonOf(t, r), jsonOf(t, want); g != w {
					t.Errorf("run row =\n%s\nwant\n%s", g, w)
				}
				if strings.Contains(string(r.Config), "stored-secret") {
					t.Errorf("the run row holds the IMAP password: %s", r.Config)
				}
				// The file holds the text form with the time, at the run level. logtest drops the time and logs at DEBUG.
				stamp := func(at time.Time, line string) string {
					return "time=" + at.Format("2006-01-02T15:04:05.000Z07:00") + " " + line + "\n"
				}
				wantFile := stamp(t0, `level=INFO msg="run started" run_id=1 source=serve`) + stamp(t0, "level=INFO msg=working n=1") + stamp(t0.Add(time.Minute), tt.wantEnd)
				if gotFile := readRunLog(t, cfg, 1); gotFile != wantFile {
					t.Errorf("run log =\n%s\nwant\n%s", gotFile, wantFile)
				}
				if want := "level=INFO msg=\"run started\" run_id=1 source=serve\nlevel=DEBUG msg=hidden\nlevel=INFO msg=working n=1\n" + tt.wantEnd + "\n"; base.String() != want {
					t.Errorf("base log =\n%s\nwant\n%s", base.String(), want)
				}
			})
		})
	}

	t.Run("a failed start save runs no work", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newTestStore(t)
			_ = s.Close()
			called := false
			cfg := tempConfig(t, "")

			err := recordRun(t.Context(), s, api.RunSourceCLI, cfg, testLogOptions(), func(context.Context, *slog.Logger, runHistory) error {
				called = true
				return nil
			})

			if want := "saving run: sql: database is closed"; errText(err) != want || called {
				t.Errorf("recordRun() = %v, work called %v, want %q and no work", err, called, want)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(cfg.Database), "logs")); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the logs dir: %v, want fs.ErrNotExist", err)
			}
		})
	})

	t.Run("a failed end save joins the run error", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newTestStore(t)

			err := recordRun(t.Context(), s, api.RunSourceCLI, tempConfig(t, ""), testLogOptions(), func(context.Context, *slog.Logger, runHistory) error {
				_ = s.Close()
				return failed
			})

			if want := "run failed\nsaving the end of run 1: sql: database is closed"; errText(err) != want || !errors.Is(err, failed) {
				t.Errorf("recordRun() = %q, want %q that matches the run error", errText(err), want)
			}
		})
	})

	t.Run("a failed log open ends the run row with its error and runs no work", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newTestStore(t)
			cfg := tempConfig(t, "")
			// A file named logs keeps the logs dir from being created.
			if err := os.WriteFile(filepath.Join(filepath.Dir(cfg.Database), "logs"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			called := false

			err := recordRun(t.Context(), s, api.RunSourceCLI, cfg, testLogOptions(), func(context.Context, *slog.Logger, runHistory) error {
				called = true
				return nil
			})

			if err == nil || !strings.HasPrefix(err.Error(), "creating the run log dir: ") || called {
				t.Errorf("recordRun() = %v, work called %v, want the log dir error and no work", err, called)
			}
			if r := readRun(t, s, 1); r.EndedAt.IsZero() || r.Error != errText(err) {
				t.Errorf("run row = %+v, want an ended row with the error %q", r, errText(err))
			}
		})
	})
}

// testLogOptions returns run log options that log to no other output.
func testLogOptions() runLogOptions {
	return runLogOptions{base: logtest.New(io.Discard).Handler(), level: slog.LevelInfo}
}

// errText returns the text of err, or "" for nil.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// A failed row save changes neither the error of the attempt nor its account.
func TestFinishHistorySaveFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestStore(t)
		var buf bytes.Buffer
		a := &attempt{
			run:    &runner{history: runHistory{accounts: s, runID: 99}},
			logger: logtest.New(&buf),
			worker: workerState{worker: 2},
			steps:  newStepTracker(time.Now(), "submit_password", phaseHTTP),
		}
		time.Sleep(time.Second)

		err := a.finish(t.Context(), &account.Account{Email: "user@example.com", Username: "user", Password: "pw"}, nil, nil, nil)

		if err != nil {
			t.Errorf("finish() = %v, want nil", err)
		}
		// The driver's error code stays out of the match, as in the store tests.
		const want = `level=INFO msg="account created" outcome=success email=user@example.com username=user 2fa=false proxy="" duration=1s steps.submit_password=1s` + "\n" +
			`level=WARN msg="attempt not saved to the run history" run_id=99 worker=2 error=`
		got := buf.String()
		rest, ok := strings.CutPrefix(got, want)
		if !ok || !strings.Contains(rest, "FOREIGN KEY constraint failed") || strings.Count(got, "\n") != 2 {
			t.Errorf("log =\n%s\nwant two lines: the record line, then the WARN up to error= and a foreign key error\n%s", got, want)
		}
		if got := readRecords(t, s); !strings.HasPrefix(got, `{"email":"user@example.com"`) {
			t.Errorf("export = %q, want the account", got)
		}
	})
}
