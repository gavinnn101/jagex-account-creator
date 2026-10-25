package main

import (
	"context"
	"sync"
)

// runFunc is the work of one mode. ctx is the soft stage. A close of stopNow is the stop-at-once stage.
type runFunc func(ctx context.Context, stopNow <-chan struct{}) error

// runControl holds the two stop stages of one run and its result.
type runControl struct {
	cancel  context.CancelFunc
	now     chan struct{}
	nowOnce sync.Once
	done    chan struct{}
	err     error
}

// startRun runs fn in the background, so a caller can stop it while it runs.
// The end of fn cancels its soft context, so a run that ends on its own frees its context tree.
func startRun(parent context.Context, fn runFunc) *runControl {
	ctx, cancel := context.WithCancel(parent)
	c := &runControl{cancel: cancel, now: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(c.done)
		defer cancel()
		c.err = fn(ctx, c.now)
	}()
	return c
}

// Stop cancels the soft context, so the work in flight finishes.
func (c *runControl) Stop() {
	c.cancel()
}

// StopNow cancels the soft context and closes the stop-at-once channel. A second call does nothing.
func (c *runControl) StopNow() {
	c.cancel()
	c.nowOnce.Do(func() { close(c.now) })
}

// Wait waits for the end of the run and returns the error of fn.
func (c *runControl) Wait() error {
	<-c.done
	return c.err
}
