package main

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	"jagex_account_creator/internal/email"
	"jagex_account_creator/internal/proxy"
)

const (
	// maxConsecutiveFailures is the number of failures in a row of one account
	// number that stop the worker with an error. A permanent error (bad config, an
	// unreachable mailbox) then stops the run, and a transient error gets retries.
	// Limitation: a cooled retry never counts, so a pool whose every proxy fails its check waits on cooldowns and never trips the limit.
	maxConsecutiveFailures = 10

	// backoffInitial is the delay after the first failure. Each subsequent
	// consecutive failure doubles the delay, up to backoffMax.
	backoffInitial = 5 * time.Second
	backoffMax     = 60 * time.Second
)

// createFunc creates one account through the proxy p with the email provider
// of its worker. ws is the handle of that worker on the run state. The attempt
// holds slot, a browser slot, while it runs.
// A nil return, or an error that matches errAccountWritten, means that the
// account is in the account store. The worker then takes the next account number.
// The workers take it as a value so that the tests can inject a fake.
type createFunc func(ctx context.Context, logger *slog.Logger, ws workerState, emailProvider email.Provider, p proxy.Proxy, slot browserSlot) error

// browserSlot is one held browser slot. waitStart is the time the wait for the
// slot began. release frees the slot, and only its first call counts.
type browserSlot struct {
	waitStart time.Time
	release   func()
}

// acquireBrowserSlot waits for a free browser slot of sem. The wait began at
// waitStart. It fails only when ctx is done, at entry or during the wait.
func acquireBrowserSlot(ctx context.Context, sem *semaphore.Weighted, waitStart time.Time) (browserSlot, error) {
	if err := sem.Acquire(ctx, 1); err != nil {
		return browserSlot{}, fmt.Errorf("waiting for a browser slot: %w", err)
	}
	return browserSlot{waitStart: waitStart, release: sync.OnceFunc(func() { sem.Release(1) })}, nil
}

// workerLoop runs one worker's attempts. createFn makes one attempt, and bo
// spaces the attempts after a failure. ws marks each backoff of the worker.
type workerLoop func(ctx context.Context, logger *slog.Logger, ws workerState, createFn func(ctx context.Context, logger *slog.Logger) error, bo workerBackoff) error

// startUnlimitedWorkers starts the unlimited mode: each worker loops until
// context is cancelled.
func (r *runner) startUnlimitedWorkers(ctx context.Context, g *errgroup.Group, logger *slog.Logger, create createFunc) {
	r.startWorkers(ctx, g, logger, r.cfg.Concurrency.MaxWorkers, create, unlimitedWorkerLoop)
}

// startFiniteWorkers starts the finite mode: it distributes accountCount work
// items across workers.
func (r *runner) startFiniteWorkers(ctx context.Context, g *errgroup.Group, logger *slog.Logger, accountCount int, create createFunc) {
	numWorkers := min(r.cfg.Concurrency.MaxWorkers, accountCount)

	work := make(chan int, numWorkers)
	go feedWork(ctx, work, accountCount)

	r.startWorkers(ctx, g, logger, numWorkers, create, func(ctx context.Context, logger *slog.Logger, ws workerState, createFn func(ctx context.Context, logger *slog.Logger) error, bo workerBackoff) error {
		return finiteWorkerLoop(ctx, logger, ws, work, createFn, bo)
	})
}

// startWorkers starts numWorkers workers on g. Each worker runs loop. Each
// attempt of the loop picks a proxy of the run's proxy manager, waits for a
// browser slot, then runs create under the per-account timeout. Each worker
// marks its waits and its attempts on the run state, and it is idle on its exit.
func (r *runner) startWorkers(ctx context.Context, g *errgroup.Group, logger *slog.Logger, numWorkers int, create createFunc, loop workerLoop) {
	sem := semaphore.NewWeighted(int64(r.cfg.Concurrency.MaxBrowsers))
	bo := defaultWorkerBackoff()
	r.state.start(numWorkers)
	for i := range numWorkers {
		workerLog := logger.With("worker", i)
		ws := workerState{state: r.state, worker: i}
		g.Go(func() error {
			defer ws.idle()
			// Each worker claims its own mailbox, so the provider's log lines carry the worker.
			mailbox := &workerMailbox{pool: r.mailboxes, logger: workerLog}

			return loop(ctx, workerLog, ws, func(ctx context.Context, logger *slog.Logger) error {
				emailProvider, err := mailbox.get(ctx)
				if err != nil {
					return err
				}
				// The proxy wait and the slot wait come before the per-account timeout, so they take no time from the attempt.
				// Limitation: with max_browsers below max_workers, a worker holds its proxy during the slot wait. A cooldown set in that wait does not stop the attempt. The waiting attempt counts as in flight on its proxy, so the share of the running attempts can skew toward the proxies of the waiters.
				ws.waitProxy()
				// Limitation: a proxy-wait row has no until, because Next does not return the earliest cooldown end. It fails toward a wait with no end time shown.
				p, release, err := r.proxyMgr.Next(ctx)
				if err != nil {
					return fmt.Errorf("waiting for a proxy: %w", err)
				}
				defer release()
				waitStart := time.Now()
				ws.attemptStarted(p, waitStart)
				slot, err := acquireBrowserSlot(ctx, sem, waitStart)
				if err != nil {
					return err
				}
				defer slot.release()
				acctCtx, acctCancel := context.WithTimeout(ctx, r.cfg.Concurrency.AccountTimeout)
				defer acctCancel()
				err = create(acctCtx, logger, ws, emailProvider, p, slot)
				mailbox.done(err)
				return err
			}, bo)
		})
	}
}

// feedWork sends the account numbers 1 to count on work and then closes it.
// It stops early when ctx ends.
func feedWork(ctx context.Context, work chan<- int, count int) {
	defer close(work)
	for i := range count {
		select {
		case work <- i + 1:
		case <-ctx.Done():
			return
		}
	}
}

// finiteWorkerLoop feeds the account numbers on work to runAttempts and
// returns its error.
func finiteWorkerLoop(ctx context.Context, logger *slog.Logger, ws workerState, work <-chan int, createFn func(ctx context.Context, logger *slog.Logger) error, bo workerBackoff) error {
	accounts := func(yield func(int) bool) {
		for n := range work {
			if !yield(n) {
				return
			}
		}
	}
	_, err := runAttempts(ctx, logger, ws, accounts, createFn, bo)
	return err
}

// workerBackoff holds exponential backoff parameters for worker retry loops.
type workerBackoff struct {
	initial time.Duration
	max     time.Duration
}

// defaultWorkerBackoff returns the production backoff configuration.
func defaultWorkerBackoff() workerBackoff {
	return workerBackoff{initial: backoffInitial, max: backoffMax}
}

// delay returns the exponential backoff delay after consecutiveFailures
// failures in a row. The delay starts at wb.initial and doubles each time,
// capped at wb.max, with ±25% jitter.
func (wb workerBackoff) delay(consecutiveFailures int) time.Duration {
	// Each step doubles below the cap, so no failure count can overflow the delay.
	delay := min(wb.initial, wb.max)
	for range consecutiveFailures - 1 {
		delay = min(2*delay, wb.max)
	}
	// Apply ±25% jitter.
	jitter := time.Duration(rand.Int64N(int64(delay)/2)) - delay/4
	return delay + jitter
}

// sleep pauses for delay, the backoff after consecutiveFailures failures in a
// row. Returns the context error if the context is cancelled during the sleep,
// or nil on normal completion.
func (wb workerBackoff) sleep(ctx context.Context, logger *slog.Logger, delay time.Duration, consecutiveFailures int) error {
	logger.Warn("backing off before next attempt", "delay", delay.Round(time.Millisecond), "consecutive_failures", consecutiveFailures)

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("backing off: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

// unlimitedWorkerLoop runs createFn for the account numbers 1, 2, 3 and on
// until ctx ends. Then it returns nil. maxConsecutiveFailures failures in a
// row of one account stop it with an error.
func unlimitedWorkerLoop(ctx context.Context, logger *slog.Logger, ws workerState, createFn func(ctx context.Context, logger *slog.Logger) error, bo workerBackoff) error {
	accounts := func(yield func(int) bool) {
		for n := 1; ; n++ {
			if !yield(n) {
				return
			}
		}
	}
	cancelled, err := runAttempts(ctx, logger, ws, accounts, createFn, bo)
	if cancelled {
		return nil
	}
	return err
}

// runAttempts runs attemptAccount for each account number of accounts, and
// stops at the first error. When ctx ends, it returns true and ctx.Err().
func runAttempts(ctx context.Context, logger *slog.Logger, ws workerState, accounts iter.Seq[int], createFn func(ctx context.Context, logger *slog.Logger) error, bo workerBackoff) (cancelled bool, err error) {
	for acctNum := range accounts {
		cancelled, err = attemptAccount(ctx, logger, ws, acctNum, createFn, bo)
		if err != nil {
			return cancelled, err
		}
	}
	return false, nil
}

// attemptAccount runs createFn for the account acctNum until the account is in
// the account store. An error that cools the proxy retries at once and does not
// count. Any other failure counts and retries after a backoff on bo, which ws
// marks. maxConsecutiveFailures failures stop the worker with an error.
// cancelled is true only when ctx ended before or during an attempt or a
// backoff. So a success stays a success when ctx ends right after it.
func attemptAccount(ctx context.Context, logger *slog.Logger, ws workerState, acctNum int, createFn func(ctx context.Context, logger *slog.Logger) error, bo workerBackoff) (cancelled bool, err error) {
	acctLog := logger.With("account", acctNum)
	failures := 0
	for {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		err = createFn(ctx, acctLog)
		if err == nil || errors.Is(err, errAccountWritten) {
			return false, nil
		}
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		if errors.Is(err, errProxyCooled) {
			continue
		}
		// A failed save also counts and retries.
		// Limitation: a store that keeps failing creates up to maxConsecutiveFailures Jagex accounts that the store does not hold before the limit stops the worker. finish keeps the cooled mark on a failed save after a 429, so that save does not count.
		failures++
		if failures >= maxConsecutiveFailures {
			return false, fmt.Errorf("worker stopped: account %d: %d consecutive failures (last: %w)", acctNum, failures, err)
		}
		delay := bo.delay(failures)
		ws.backoff(delay)
		if bo.sleep(ctx, logger, delay, failures) != nil {
			return true, ctx.Err()
		}
	}
}
