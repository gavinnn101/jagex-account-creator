package main

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

// startIdle starts a run whose work waits for the end of its soft context. It
// returns the soft context and the stop-at-once channel that the work got.
func startIdle(t *testing.T) (c *runControl, soft context.Context, stopNow <-chan struct{}) {
	t.Helper()
	ctxs := make(chan context.Context, 1)
	c = startRun(t.Context(), func(ctx context.Context, now <-chan struct{}) error {
		stopNow = now
		ctxs <- ctx
		<-ctx.Done()
		return nil
	})
	soft = <-ctxs
	return c, soft, stopNow
}

func TestRunControl(t *testing.T) {
	tests := []struct {
		name    string
		stop    func(c *runControl)
		wantNow bool
	}{
		{name: "Stop alone", stop: func(c *runControl) { c.Stop() }},
		{name: "StopNow", stop: func(c *runControl) { c.StopNow() }, wantNow: true},
		{name: "Stop then StopNow", stop: func(c *runControl) { c.Stop(); c.StopNow() }, wantNow: true},
		{name: "StopNow twice", stop: func(c *runControl) { c.StopNow(); c.StopNow() }, wantNow: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, soft, stopNow := startIdle(t)
				if soft.Err() != nil || isClosed(stopNow) {
					t.Fatal("the run started stopped")
				}

				tt.stop(c)

				if !errors.Is(soft.Err(), context.Canceled) {
					t.Errorf("soft Err() = %v, want context.Canceled", soft.Err())
				}
				if got := isClosed(stopNow); got != tt.wantNow {
					t.Errorf("stopNow closed = %v, want %v", got, tt.wantNow)
				}
				if err := c.Wait(); err != nil {
					t.Errorf("Wait() = %v, want nil", err)
				}
			})
		})
	}

	t.Run("Wait returns the error of fn", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			want := errors.New("run failed")
			ctxs := make(chan context.Context, 1)
			c := startRun(t.Context(), func(ctx context.Context, _ <-chan struct{}) error {
				ctxs <- ctx
				return want
			})

			if err := c.Wait(); !errors.Is(err, want) {
				t.Errorf("Wait() = %v, want %v", err, want)
			}
			if soft := <-ctxs; !errors.Is(soft.Err(), context.Canceled) {
				t.Errorf("soft Err() after the run = %v, want context.Canceled", soft.Err())
			}
		})
	})

	t.Run("a stop after the run ended", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			want := errors.New("run failed")
			c := startRun(t.Context(), func(context.Context, <-chan struct{}) error { return want })
			if err := c.Wait(); !errors.Is(err, want) {
				t.Fatalf("Wait() = %v, want %v", err, want)
			}

			c.Stop()
			c.StopNow()

			if err := c.Wait(); !errors.Is(err, want) {
				t.Errorf("Wait() after the stops = %v, want %v", err, want)
			}
		})
	})
}
