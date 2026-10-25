package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"jagex_account_creator/internal/config"
	"jagex_account_creator/internal/jagexapi"
	"jagex_account_creator/internal/logtest"
	"jagex_account_creator/internal/waittest"
)

func TestWarnChromeMajor(t *testing.T) {
	t.Run("same major", func(t *testing.T) {
		var buf bytes.Buffer
		warnChromeMajor(logtest.New(&buf), fmt.Sprintf("%d.0.8059.40", jagexapi.ShapeChromeMajor))
		if buf.Len() != 0 {
			t.Errorf("the shapes' major logged:\n%s", buf.String())
		}
	})
	t.Run("other major", func(t *testing.T) {
		var buf bytes.Buffer
		version := fmt.Sprintf("%d.0.8059.40", jagexapi.ShapeChromeMajor+1)
		warnChromeMajor(logtest.New(&buf), version)
		want := fmt.Sprintf(`level=WARN msg="installed Chrome major differs from the Chrome that the request shapes copy" chrome_version=%s shape_major=%d`+"\n",
			version, jagexapi.ShapeChromeMajor)
		if got := buf.String(); !strings.HasSuffix(got, want) || strings.Count(got, "\n") != 1 {
			t.Errorf("log = %q, want one line ending %q", got, want)
		}
	})
}

// fakeChrome returns a binary whose version InstalledChrome reads and whose
// major is not the shapes' major. Windows reads the version resource of
// cmd.exe. Unix runs a script that prints a version.
func fakeChrome(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	}
	bin := filepath.Join(t.TempDir(), "chrome")
	script := fmt.Sprintf("#!/bin/sh\necho 'Google Chrome %d.0.8059.40'\n", jagexapi.ShapeChromeMajor+1)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// junkChrome returns a binary whose version read fails. A 0644 file has no
// version resource on Windows, and exec refuses it on Unix.
func junkChrome(t *testing.T) string {
	t.Helper()
	junk := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(junk, []byte("not a browser"), 0o644); err != nil {
		t.Fatal(err)
	}
	return junk
}

func TestResolveUserAgent(t *testing.T) {
	cfg := &config.Config{Chrome: config.ChromeConfig{Headless: new(false)}}

	t.Run("a headful run keeps Chrome's own UA", func(t *testing.T) {
		t.Setenv("CHROME_PATH", fakeChrome(t))
		var buf bytes.Buffer

		override, err := resolveUserAgent(t.Context(), logtest.New(&buf), cfg)

		if override != nil || err != nil {
			t.Fatalf("resolveUserAgent() = %+v, %v, want nil, nil", override, err)
		}
		const warn = `level=WARN msg="installed Chrome major differs from the Chrome that the request shapes copy" chrome_version=`
		if !strings.HasPrefix(buf.String(), warn) || strings.Count(buf.String(), "\n") != 1 {
			t.Errorf("log = %q, want one line starting %q", buf.String(), warn)
		}
	})

	t.Run("a junk binary fails", func(t *testing.T) {
		t.Setenv("CHROME_PATH", junkChrome(t))
		var buf bytes.Buffer

		override, err := resolveUserAgent(t.Context(), logtest.New(&buf), cfg)

		if override != nil || err == nil || !strings.HasPrefix(err.Error(), "getting chrome version: ") {
			t.Fatalf("resolveUserAgent() = %+v, %v, want nil and a version error", override, err)
		}
		if buf.Len() != 0 {
			t.Errorf("a failed version read logged:\n%s", buf.String())
		}
	})
}

// isClosed reports whether ch is closed.
func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestForwardSignals(t *testing.T) {
	const (
		softLine = `level=INFO msg="stopping: the work in flight finishes. Press Ctrl+C again to stop at once"` + "\n"
		nowLine  = `level=INFO msg="stopping at once"` + "\n"
	)
	tests := []struct {
		name     string
		send     []os.Signal
		wantSoft bool
		wantNow  bool
		wantLog  string
	}{
		{name: "one SIGINT", send: []os.Signal{os.Interrupt}, wantSoft: true, wantLog: softLine},
		{name: "two SIGINTs", send: []os.Signal{os.Interrupt, os.Interrupt}, wantSoft: true, wantNow: true, wantLog: softLine + nowLine},
		{name: "SIGTERM", send: []os.Signal{syscall.SIGTERM}, wantSoft: true, wantNow: true, wantLog: nowLine},
		{name: "release with no signal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var buf bytes.Buffer
				sigs := make(chan os.Signal, 2)
				c, soft, stopNow := startIdle(t)
				release := forwardSignals(logtest.New(&buf), sigs, c)
				check := func(when string) {
					if got := soft.Err() != nil; got != tt.wantSoft {
						t.Errorf("%s: soft ended = %v, want %v", when, got, tt.wantSoft)
					}
					if got := isClosed(stopNow); got != tt.wantNow {
						t.Errorf("%s: stopNow closed = %v, want %v", when, got, tt.wantNow)
					}
					if got := buf.String(); got != tt.wantLog {
						t.Errorf("%s: log =\n%s\nwant\n%s", when, got, tt.wantLog)
					}
				}

				for _, sig := range tt.send {
					sigs <- sig
				}
				synctest.Wait()
				check("after the signals")

				// A signal after release reaches no stage, and the end of the bubble asserts that the read ended.
				release()
				sigs <- syscall.SIGTERM
				synctest.Wait()
				check("after release")

				c.Stop()
				if err := c.Wait(); err != nil {
					t.Errorf("Wait() = %v, want nil", err)
				}
			})
		})
	}
}

func TestStopNowContext(t *testing.T) {
	tests := []struct {
		name    string
		nilStop bool
		act     func(cancelParent context.CancelFunc, stopNow chan struct{})
		wantErr error
	}{
		{name: "a parent cancel does not end it", act: func(cancelParent context.CancelFunc, _ chan struct{}) { cancelParent() }},
		{name: "the parent deadline does not end it", act: func(context.CancelFunc, chan struct{}) { time.Sleep(2 * time.Second) }},
		{name: "a close of stopNow ends it", act: func(_ context.CancelFunc, stopNow chan struct{}) { close(stopNow) }, wantErr: context.Canceled},
		{name: "a nil stopNow never ends it", nilStop: true, act: func(_ context.CancelFunc, stopNow chan struct{}) { close(stopNow) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parent, cancelParent := context.WithTimeout(t.Context(), time.Second)
				defer cancelParent()
				stopNow := make(chan struct{})
				var stop <-chan struct{} = stopNow
				if tt.nilStop {
					stop = nil
				}
				detached, cancel := stopNowContext(parent, stop)

				tt.act(cancelParent, stopNow)
				synctest.Wait()

				if err := detached.Err(); !errors.Is(err, tt.wantErr) {
					t.Errorf("Err() = %v, want %v", err, tt.wantErr)
				}
				if _, ok := detached.Deadline(); ok {
					t.Error("the context has the parent's deadline")
				}
				// A cancel with no stop must also end the goroutine. The end of the bubble asserts it.
				cancel()
				if !errors.Is(detached.Err(), context.Canceled) {
					t.Errorf("Err() after cancel = %v, want context.Canceled", detached.Err())
				}
			})
		})
	}
}

func TestModeFunc(t *testing.T) {
	// Each run fails at its first step: the full run at the set-up of the store on
	// the ended context, before the mailbox check, the challenge run at the version
	// read of the junk binary, the export run at the set-up of the store, so its
	// -out reached it, and the serve run, on a live context, at the listen on the
	// bad port of its -listen after its store opened.
	t.Setenv("CHROME_PATH", junkChrome(t))
	tests := []struct {
		mode       string
		live       bool
		wantPrefix string
	}{
		{mode: "full", wantPrefix: "setting up database "},
		{mode: "challenge", wantPrefix: "getting chrome version: "},
		{mode: "export", wantPrefix: "setting up database "},
		{mode: "serve", live: true, wantPrefix: "listening for the API: "},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			var buf bytes.Buffer
			dir := t.TempDir()
			opts := cliOptions{mode: tt.mode, out: filepath.Join(dir, "export.jsonl"), listen: "127.0.0.1:-1"}
			fn, err := modeFunc(opts, logtest.New(&buf), &slog.LevelVar{}, &config.Config{Database: filepath.Join(dir, "creator.db")}, builtDashboard)
			if fn == nil || err != nil {
				t.Fatalf("modeFunc(%q): fn is nil = %v, error = %v, want a fn and no error", tt.mode, fn == nil, err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if !tt.live {
				cancel()
			}
			if err := fn(ctx, nil); err == nil || !strings.HasPrefix(err.Error(), tt.wantPrefix) {
				t.Errorf("the %s run error = %v, want the prefix %q", tt.mode, err, tt.wantPrefix)
			}
			if buf.Len() != 0 {
				t.Errorf("the %s run logged before its first step failed:\n%s", tt.mode, buf.String())
			}
		})
	}
	t.Run("unknown", func(t *testing.T) {
		fn, err := modeFunc(cliOptions{mode: "other"}, logtest.New(io.Discard), &slog.LevelVar{}, &config.Config{}, builtDashboard)
		if want := `unknown mode "other"`; fn != nil || err == nil || err.Error() != want {
			t.Errorf("modeFunc(\"other\"): fn is nil = %v, error = %v, want no fn and %q", fn == nil, err, want)
		}
	})
}

// The full mode saves its run row before the mailbox check, so a run that fails that check is in the history.
func TestRunCLI(t *testing.T) {
	// The mailbox server closes each connection, so the TLS dial of the check fails at once.
	ln := listenLoopback(t)
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	})
	t.Cleanup(func() {
		_ = ln.Close()
		waittest.Wait(t, &wg)
	})
	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	cfg := storedConfig(t, "stored")
	cfg.Database = filepath.Join(t.TempDir(), "creator.db")
	cfg.Email.IMAPHost = host
	if cfg.Email.IMAPPort, err = strconv.Atoi(port); err != nil {
		t.Fatal(err)
	}

	var level slog.LevelVar
	level.Set(slog.LevelWarn)
	err = runCLI(t.Context(), nil, logtest.New(io.Discard), &level, cfg)

	const prefix = "checking the mailbox: connecting to IMAP: "
	if err == nil || !strings.HasPrefix(err.Error(), prefix) {
		t.Fatalf("runCLI() = %v, want the prefix %q", err, prefix)
	}
	// The CLI run writes its log file at the shared level, so the INFO lines stay out of it.
	if file := readRunLog(t, cfg, 1); file != "" {
		t.Errorf("run log = %q, want no line at WARN", file)
	}
	s, err := openStore(t.Context(), logtest.New(io.Discard), cfg)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if r := readRun(t, s, 1); r.Source != "cli" || r.EndedAt.IsZero() || !strings.HasPrefix(r.Error, prefix) {
		t.Errorf("run row = %+v, want an ended cli run with the mailbox error", r)
	}
}

// The first signal.Notify of the process starts a runtime goroutine that never
// ends, so this test runs outside a bubble. Its fake run returns at once.
func TestRunWithSignals(t *testing.T) {
	want := errors.New("run failed")
	var buf bytes.Buffer
	err := runWithSignals(t.Context(), logtest.New(&buf), func(ctx context.Context, stopNow <-chan struct{}) error {
		if ctx.Err() != nil || stopNow == nil || isClosed(stopNow) {
			return errors.New("the run started stopped")
		}
		return want
	})
	if !errors.Is(err, want) {
		t.Errorf("runWithSignals() = %v, want %v", err, want)
	}
	if buf.Len() != 0 {
		t.Errorf("a run with no signal logged:\n%s", buf.String())
	}
}
