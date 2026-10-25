package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"golang.org/x/sync/errgroup"

	"jagex_account_creator/internal/api"
	"jagex_account_creator/internal/browser"
	"jagex_account_creator/internal/config"
	"jagex_account_creator/internal/email"
	"jagex_account_creator/internal/jagexapi"
	"jagex_account_creator/internal/logtest"
	"jagex_account_creator/internal/proxy"
)

// TestWorkChannelIterator verifies feedWork, the producer of the finite-mode
// work channel: a small-buffered channel fed by a goroutine that respects
// context cancellation.
func TestWorkChannelIterator(t *testing.T) {
	t.Run("delivers all items", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			const accountCount = 100
			const numWorkers = 4

			ctx := t.Context()

			work := make(chan int, numWorkers)
			go feedWork(ctx, work, accountCount)

			var received []int
			for v := range work {
				received = append(received, v)
			}

			if len(received) != accountCount {
				t.Fatalf("got %d items, want %d", len(received), accountCount)
			}
			for i, v := range received {
				if v != i+1 {
					t.Fatalf("item %d = %d, want %d", i, v, i+1)
				}
			}
		})
	})

	t.Run("stops on context cancellation", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			const accountCount = 100000
			const numWorkers = 2

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			work := make(chan int, numWorkers)
			go feedWork(ctx, work, accountCount)

			// Drain a few items then cancel.
			count := 0
			for range work {
				count++
				if count >= 10 {
					cancel()
					break
				}
			}

			// Drain remaining items after cancel — channel should close promptly.
			for range work {
				count++
			}

			if count >= accountCount {
				t.Fatalf("expected early stop, got all %d items", count)
			}
		})
	})
}

// fastBackoff returns a workerBackoff with negligible delays for fast tests.
func fastBackoff() workerBackoff {
	return workerBackoff{initial: time.Millisecond, max: 5 * time.Millisecond}
}

func TestUnlimitedWorkerLoop(t *testing.T) {
	discardLogger := slog.New(slog.DiscardHandler)

	t.Run("returns error after maxConsecutiveFailures", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			calls := 0
			createFn := func(_ context.Context, _ *slog.Logger) error {
				calls++
				return errors.New("permanent failure")
			}

			err := unlimitedWorkerLoop(context.Background(), discardLogger, workerState{}, createFn, fastBackoff())
			if calls != maxConsecutiveFailures {
				t.Errorf("calls = %d, want %d", calls, maxConsecutiveFailures)
			}
			const want = "worker stopped: account 1: 10 consecutive failures (last: permanent failure)"
			if err == nil || err.Error() != want {
				t.Errorf("error = %v, want %s", err, want)
			}
		})
	})

	t.Run("success advances the account and the next account counts its own failures", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			calls := 0
			createFn := func(_ context.Context, _ *slog.Logger) error {
				calls++
				if calls > 30 {
					return errors.New("final failure")
				}
				if calls%maxConsecutiveFailures == 0 {
					return nil
				}
				return errors.New("transient failure")
			}

			err := unlimitedWorkerLoop(context.Background(), discardLogger, workerState{}, createFn, fastBackoff())
			// Three successes end accounts 1-3, so account 4 takes the ten failures after call 30.
			if want := 30 + maxConsecutiveFailures; calls != want {
				t.Errorf("calls = %d, want %d", calls, want)
			}
			const want = "worker stopped: account 4: 10 consecutive failures (last: final failure)"
			if err == nil || err.Error() != want {
				t.Errorf("error = %v, want %s", err, want)
			}
		})
	})

	t.Run("returns nil on context cancellation", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			calls := 0
			createFn := func(_ context.Context, _ *slog.Logger) error {
				calls++
				if calls >= 3 {
					cancel()
				}
				return errors.New("failure")
			}

			err := unlimitedWorkerLoop(ctx, discardLogger, workerState{}, createFn, fastBackoff())
			if err != nil {
				t.Errorf("expected nil on context cancellation, got: %v", err)
			}
		})
	})

	t.Run("returns nil on context cancellation before loop", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		calls := 0
		createFn := func(_ context.Context, _ *slog.Logger) error {
			calls++
			return nil
		}

		err := unlimitedWorkerLoop(ctx, discardLogger, workerState{}, createFn, fastBackoff())
		if err != nil {
			t.Errorf("expected nil, got: %v", err)
		}
		if calls != 0 {
			t.Errorf("calls = %d, expected 0 (should not call createFn after cancellation)", calls)
		}
	})

	t.Run("returns nil on context cancellation during failure", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		createFn := func(_ context.Context, _ *slog.Logger) error {
			cancel() // cancel during the call
			return errors.New("failure")
		}

		err := unlimitedWorkerLoop(ctx, discardLogger, workerState{}, createFn, fastBackoff())
		if err != nil {
			t.Errorf("expected nil on context cancellation, got: %v", err)
		}
	})

	t.Run("last error is wrapped in returned error", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			sentinel := errors.New("sentinel error")
			calls := 0
			createFn := func(_ context.Context, _ *slog.Logger) error {
				calls++
				if calls == maxConsecutiveFailures {
					return fmt.Errorf("wrapping: %w", sentinel)
				}
				return errors.New("other failure")
			}

			err := unlimitedWorkerLoop(context.Background(), discardLogger, workerState{}, createFn, fastBackoff())
			if !errors.Is(err, sentinel) {
				t.Errorf("expected error to wrap sentinel, got: %v", err)
			}
		})
	})

	t.Run("continuous success never returns", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		createFn := func(_ context.Context, _ *slog.Logger) error {
			calls++
			if calls >= 100 {
				cancel()
			}
			return nil
		}

		err := unlimitedWorkerLoop(ctx, discardLogger, workerState{}, createFn, fastBackoff())
		if err != nil {
			t.Errorf("expected nil, got: %v", err)
		}
		if calls != 100 {
			t.Errorf("calls = %d, want 100", calls)
		}
	})
}

// loopError is an error that a fake attempt returns to a worker loop.
type loopError struct {
	name   string
	err    error
	cooled bool // the attempt put its proxy in cooldown
}

// loopErrors returns the cooled errors, which retry the account at once, and
// the same errors of a direct connection, which back off and count.
func loopErrors() []loopError {
	proxyErr := fmt.Errorf("proxy check: %w", browser.ErrProxyConnection)
	rateErr := fmt.Errorf("initial registration: %w: status 429", jagexapi.ErrRateLimited)
	return []loopError{
		{name: "cooled proxy error", err: markedError{err: proxyErr, mark: errProxyCooled}, cooled: true},
		{name: "cooled rate limit", err: markedError{err: rateErr, mark: errProxyCooled}, cooled: true},
		{name: "proxy error of a direct connection", err: proxyErr},
		{name: "rate limit of a direct connection", err: rateErr},
	}
}

func TestWorkerLoopsCooledProxy(t *testing.T) {
	loops := []struct {
		name string
		run  workerLoop
	}{
		{name: "unlimited", run: unlimitedWorkerLoop},
		{name: "finite", run: func(ctx context.Context, logger *slog.Logger, ws workerState, createFn func(ctx context.Context, logger *slog.Logger) error, bo workerBackoff) error {
			return finiteWorkerLoop(ctx, logger, ws, queuedWork(2), createFn, bo)
		}},
	}
	for _, loop := range loops {
		for _, tt := range loopErrors() {
			t.Run(loop.name+", a "+tt.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					var log attemptLog
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					calls := 0
					// A success, a failure, the error of the case, a failure, then a success that ends the loop.
					createFn := func(_ context.Context, logger *slog.Logger) error {
						logger.Info("attempt")
						calls++
						switch calls {
						case 1:
							return nil
						case 3:
							return tt.err
						case 5:
							cancel()
							return nil
						default:
							return errors.New("permanent failure")
						}
					}

					if err := loop.run(ctx, log.logger(), workerState{}, createFn, fastBackoff()); err != nil {
						t.Fatalf("unexpected error: %v", err)
					}
					wantBackoff := "[1 2 3]"
					if tt.cooled {
						// The cooled error retries at once, and it neither sleeps nor counts.
						wantBackoff = "[1 2]"
					}
					if got := fmt.Sprint(log.accounts(t)); got != "[1 2 2 2 2]" {
						t.Errorf("accounts = %s, want [1 2 2 2 2]", got)
					}
					if got := fmt.Sprint(log.backoffCounts()); got != wantBackoff {
						t.Errorf("backoff counts = %s, want %s", got, wantBackoff)
					}
				})
			})
		}
	}
}

// checkJitter fails the test when got is outside [0.75*base, 1.25*base), the
// range of the ±25% backoff jitter.
func checkJitter(t *testing.T, name string, got, base time.Duration) {
	t.Helper()
	if got < base-base/4 || got >= base+base/4 {
		t.Errorf("%s = %v, want in [%v, %v)", name, got, base-base/4, base+base/4)
	}
}

func TestWorkerBackoffDelay(t *testing.T) {
	t.Run("exponential increase", func(t *testing.T) {
		bo := workerBackoff{initial: 50 * time.Millisecond, max: 10 * time.Second}
		for i, base := range []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond} {
			checkJitter(t, fmt.Sprintf("delay %d", i+1), bo.delay(i+1), base)
		}
	})

	t.Run("capped at max", func(t *testing.T) {
		bo := workerBackoff{initial: 50 * time.Millisecond, max: 80 * time.Millisecond}
		checkJitter(t, "delay", bo.delay(10), 80*time.Millisecond)
	})

	t.Run("a long failure run stays at max", func(t *testing.T) {
		bo := defaultWorkerBackoff()
		// 31 is the last count whose doubling of the 5 s initial fits in int64. At 32 and 64 it overflows.
		for _, n := range []int{31, 32, 64} {
			checkJitter(t, fmt.Sprintf("delay %d", n), bo.delay(n), backoffMax)
		}
	})
}

func TestWorkerBackoffSleep(t *testing.T) {
	discardLogger := slog.New(slog.DiscardHandler)

	t.Run("sleeps the delay", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			if err := defaultWorkerBackoff().sleep(t.Context(), discardLogger, 3*time.Second, 1); err != nil {
				t.Fatal(err)
			}
			if elapsed := time.Since(start); elapsed != 3*time.Second {
				t.Errorf("took %v, want exactly 3s", elapsed)
			}
		})
	})

	t.Run("context cancellation interrupts sleep", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			bo := workerBackoff{initial: 10 * time.Second, max: 10 * time.Second}
			ctx, cancel := context.WithCancel(t.Context())
			start := time.Now()
			done := make(chan error, 1)
			go func() {
				done <- bo.sleep(ctx, discardLogger, 10*time.Second, 1)
			}()

			// Wait returns once the sleep is blocked in its select.
			synctest.Wait()
			cancel()

			err := <-done
			if !errors.Is(err, context.Canceled) || err.Error() != "backing off: context canceled" {
				t.Errorf("error = %v, want backing off: context canceled", err)
			}
			if elapsed := time.Since(start); elapsed != 0 {
				t.Errorf("took %v, want 0: the cancel ends the backoff before its timer runs", elapsed)
			}
		})
	})
}

func TestUnlimitedWorkerLoop_BackoffBetweenFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bo := workerBackoff{initial: 50 * time.Millisecond, max: 200 * time.Millisecond}
		var timestamps []time.Time
		ctx, cancel := context.WithCancel(t.Context())
		// Three failures, then a success that ends the loop.
		createFn := func(context.Context, *slog.Logger) error {
			timestamps = append(timestamps, time.Now())
			if len(timestamps) >= 4 {
				cancel()
				return nil
			}
			return errors.New("failure")
		}

		if err := unlimitedWorkerLoop(ctx, slog.New(slog.DiscardHandler), workerState{}, createFn, bo); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(timestamps) != 4 {
			t.Fatalf("calls = %d, want 4", len(timestamps))
		}
		for i, base := range []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond} {
			checkJitter(t, fmt.Sprintf("gap %d", i+1), timestamps[i+1].Sub(timestamps[i]), base)
		}
	})
}

func TestUnlimitedWorkerLoop_CancelledDuringBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		// Not DiscardHandler: it is never enabled, so Handle would not run.
		logger := slog.New(cancelOnBackoff{Handler: slog.NewTextHandler(&bytes.Buffer{}, nil), cancel: cancel})
		calls := 0
		createFn := func(context.Context, *slog.Logger) error {
			calls++
			return errors.New("failure")
		}
		slow := workerBackoff{initial: time.Minute, max: time.Minute}
		start := time.Now()
		if err := unlimitedWorkerLoop(ctx, logger, workerState{}, createFn, slow); err != nil {
			t.Errorf("error = %v, want nil", err)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1", calls)
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("took %v, want 0: the cancel ends the backoff before its timer runs", elapsed)
		}
	})
}

// attemptLog records the account number of each attempt that a fake create
// function logs as "attempt".
type attemptLog struct {
	buf bytes.Buffer
}

var attemptPattern = regexp.MustCompile(`msg=attempt account=(\d+)`)

func (a *attemptLog) logger() *slog.Logger {
	return logtest.New(&a.buf)
}

func (a *attemptLog) accounts(t *testing.T) []int {
	t.Helper()
	matches := attemptPattern.FindAllStringSubmatch(a.buf.String(), -1)
	nums := make([]int, 0, len(matches))
	for _, m := range matches {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatal(err)
		}
		nums = append(nums, n)
	}
	return nums
}

// backoffCounts returns the consecutive_failures value of each backoff log line.
func (a *attemptLog) backoffCounts() []string {
	var counts []string
	for line := range strings.Lines(a.buf.String()) {
		if !strings.Contains(line, "backing off before next attempt") {
			continue
		}
		_, after, _ := strings.Cut(line, "consecutive_failures=")
		counts = append(counts, strings.TrimSpace(after))
	}
	return counts
}

// queuedWork returns a closed channel that holds the account numbers 1 to n.
func queuedWork(n int) <-chan int {
	work := make(chan int, n)
	for i := range n {
		work <- i + 1
	}
	close(work)
	return work
}

// cancelOnBackoff cancels a context when the backoff log line is handled.
type cancelOnBackoff struct {
	slog.Handler

	cancel context.CancelFunc
}

func (h cancelOnBackoff) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "backing off before next attempt" {
		h.cancel()
	}
	return h.Handler.Handle(ctx, r) //nolint:wrapcheck // A test handler passes the inner result through.
}

func (h cancelOnBackoff) WithAttrs(attrs []slog.Attr) slog.Handler {
	return cancelOnBackoff{Handler: h.Handler.WithAttrs(attrs), cancel: h.cancel}
}

func TestFiniteWorkerLoop(t *testing.T) {
	t.Run("runs each account once", func(t *testing.T) {
		var log attemptLog
		createFn := func(_ context.Context, logger *slog.Logger) error {
			logger.Info("attempt")
			return nil
		}
		if err := finiteWorkerLoop(t.Context(), log.logger(), workerState{}, queuedWork(3), createFn, fastBackoff()); err != nil {
			t.Fatalf("finiteWorkerLoop: %v", err)
		}
		if got := fmt.Sprint(log.accounts(t)); got != "[1 2 3]" {
			t.Errorf("accounts = %s, want [1 2 3]", got)
		}
	})

	fail := errors.New("permanent failure")
	retryTests := []struct {
		name         string
		errs         []error // the error of each call. The calls after the list succeed.
		wantAccounts string
		wantBackoff  string
		wantErr      string
	}{
		{
			name:         "a failure retries its account after a backoff, and a success resets the count",
			errs:         []error{fail, fail, nil, fail},
			wantAccounts: "[1 1 1 2 2]",
			wantBackoff:  "[1 2 1]",
		},
		{
			name:         "a failed attempt whose account is in the file ends its account",
			errs:         []error{markedError{err: fail, mark: errAccountWritten}},
			wantAccounts: "[1 2]",
			wantBackoff:  "[]",
		},
		{
			name:         "a cooled attempt whose account is in the file ends its account",
			errs:         []error{markedError{err: markedError{err: fail, mark: errProxyCooled}, mark: errAccountWritten}},
			wantAccounts: "[1 2]",
			wantBackoff:  "[]",
		},
		{
			name:         "a failed save retries its account",
			errs:         []error{fmt.Errorf("%w: disk full", errSaveFailed)},
			wantAccounts: "[1 1 2]",
			wantBackoff:  "[1]",
		},
		{
			name:         "maxConsecutiveFailures failures of one account stop the loop",
			errs:         slices.Repeat([]error{fail}, maxConsecutiveFailures+1),
			wantAccounts: fmt.Sprint(slices.Repeat([]int{1}, maxConsecutiveFailures)),
			wantBackoff:  "[1 2 3 4 5 6 7 8 9]",
			wantErr:      "worker stopped: account 1: 10 consecutive failures (last: permanent failure)",
		},
	}
	for _, tt := range retryTests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var log attemptLog
				calls := 0
				createFn := func(_ context.Context, logger *slog.Logger) error {
					logger.Info("attempt")
					calls++
					if calls <= len(tt.errs) {
						return tt.errs[calls-1]
					}
					return nil
				}
				gotErr := ""
				if err := finiteWorkerLoop(t.Context(), log.logger(), workerState{}, queuedWork(2), createFn, fastBackoff()); err != nil {
					gotErr = err.Error()
				}
				if gotErr != tt.wantErr {
					t.Errorf("error = %q, want %q", gotErr, tt.wantErr)
				}
				if got := fmt.Sprint(log.accounts(t)); got != tt.wantAccounts {
					t.Errorf("accounts = %s, want %s", got, tt.wantAccounts)
				}
				if got := fmt.Sprint(log.backoffCounts()); got != tt.wantBackoff {
					t.Errorf("backoff counts = %s, want %s", got, tt.wantBackoff)
				}
			})
		})
	}

	t.Run("cancelled before an attempt", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		calls := 0
		createFn := func(context.Context, *slog.Logger) error {
			calls++
			return nil
		}
		err := finiteWorkerLoop(ctx, slog.New(slog.DiscardHandler), workerState{}, queuedWork(2), createFn, fastBackoff())
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
		if calls != 0 {
			t.Errorf("calls = %d, want 0", calls)
		}
	})

	t.Run("cancelled during an attempt", func(t *testing.T) {
		var log attemptLog
		ctx, cancel := context.WithCancel(t.Context())
		createFn := func(_ context.Context, logger *slog.Logger) error {
			logger.Info("attempt")
			cancel()
			return errors.New("failure")
		}
		err := finiteWorkerLoop(ctx, log.logger(), workerState{}, queuedWork(2), createFn, fastBackoff())
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
		if got := fmt.Sprint(log.accounts(t)); got != "[1]" {
			t.Errorf("accounts = %s, want [1]", got)
		}
		if strings.Contains(log.buf.String(), "account creation failed") {
			t.Errorf("a cancelled attempt was logged as a failure:\n%s", log.buf.String())
		}
	})

	t.Run("cancelled during the backoff", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			// Not DiscardHandler: it is never enabled, so Handle would not run.
			logger := slog.New(cancelOnBackoff{Handler: slog.NewTextHandler(&bytes.Buffer{}, nil), cancel: cancel})
			calls := 0
			createFn := func(context.Context, *slog.Logger) error {
				calls++
				return errors.New("failure")
			}
			slow := workerBackoff{initial: time.Minute, max: time.Minute}
			start := time.Now()
			err := finiteWorkerLoop(ctx, logger, workerState{}, queuedWork(2), createFn, slow)
			if !errors.Is(err, context.Canceled) {
				t.Errorf("error = %v, want context.Canceled", err)
			}
			if calls != 1 {
				t.Errorf("calls = %d, want 1", calls)
			}
			if elapsed := time.Since(start); elapsed != 0 {
				t.Errorf("took %v, want 0: the cancel ends the backoff before its timer runs", elapsed)
			}
		})
	})
}

// workerCalls records the calls that a fake create function receives.
type workerCalls struct {
	mu        sync.Mutex
	n         int
	calls     map[int]int            // the calls of each worker
	providers map[int]email.Provider // the email provider of each worker
	errs      []string
}

func newWorkerCalls() *workerCalls {
	return &workerCalls{calls: map[int]int{}, providers: map[int]email.Provider{}}
}

// create checks that each call gets the whole per-account timeout, the handle
// of a worker from 0 to numWorkers-1, one email provider for each worker and a
// held browser slot. It must run in a synctest bubble, so the time left is
// exact. after gets the call count of the run and of the worker, and its error
// is the result of the call.
func (w *workerCalls) create(timeout time.Duration, numWorkers int, after func(n, workerN int) error) createFunc {
	return func(ctx context.Context, _ *slog.Logger, ws workerState, emailProvider email.Provider, _ proxy.Proxy, slot browserSlot) error {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.n++
		if ws.worker < 0 || ws.worker >= numWorkers {
			w.errs = append(w.errs, fmt.Sprintf("worker %d, want 0 to %d", ws.worker, numWorkers-1))
		}
		if emailProvider == nil {
			w.errs = append(w.errs, "nil email provider")
		}
		if prev, ok := w.providers[ws.worker]; ok && prev != emailProvider {
			w.errs = append(w.errs, fmt.Sprintf("worker %d got a second email provider", ws.worker))
		}
		w.providers[ws.worker] = emailProvider
		if slot.release == nil || slot.waitStart.IsZero() {
			w.errs = append(w.errs, fmt.Sprintf("slot = %+v, want a release and a wait start", slot))
		}
		w.calls[ws.worker]++
		deadline, ok := ctx.Deadline()
		if left := time.Until(deadline); !ok || left != timeout {
			w.errs = append(w.errs, fmt.Sprintf("deadline %v (set %v), want exactly %v", left, ok, timeout))
		}
		if after != nil {
			return after(w.n, w.calls[ws.worker])
		}
		return nil
	}
}

func workerConfig(maxWorkers int) *config.Config {
	return &config.Config{Concurrency: config.ConcurrencyConfig{MaxWorkers: maxWorkers, MaxBrowsers: maxWorkers, AccountTimeout: time.Minute}}
}

// slotConfig returns a worker config with maxBrowsers browser slots.
func slotConfig(maxWorkers, maxBrowsers int) *config.Config {
	cfg := workerConfig(maxWorkers)
	cfg.Concurrency.MaxBrowsers = maxBrowsers
	return cfg
}

// runOnce is a worker loop that makes one attempt.
func runOnce(ctx context.Context, logger *slog.Logger, _ workerState, createFn func(ctx context.Context, logger *slog.Logger) error, _ workerBackoff) error {
	return createFn(ctx, logger)
}

// slotEntries counts the fake creates that have entered.
type slotEntries struct {
	mu sync.Mutex
	n  int
}

// enter counts one entry and returns the count.
func (e *slotEntries) enter() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.n++
	return e.n
}

func (e *slotEntries) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.n
}

// otherProxyAddr is the second proxy of a manager of two proxies.
const otherProxyAddr = "203.0.113.2:8080"

// releasedProxies picks n proxies of m in turn, releases each one before the
// next pick and returns their host:port forms.
func releasedProxies(ctx context.Context, t *testing.T, m *proxy.Manager, n int) []string {
	t.Helper()
	var got []string
	for range n {
		p, release, err := m.Next(ctx)
		if err != nil {
			t.Errorf("Next: %v", err)
			return got
		}
		got = append(got, p.HostPort())
		release()
	}
	return got
}

// assertNoLoad fails t when an attempt still holds a proxy of m, a manager of
// two proxies. With equal loads, two picks in turn return both proxies.
func assertNoLoad(t *testing.T, m *proxy.Manager) {
	t.Helper()
	if got := releasedProxies(t.Context(), t, m, 2); len(got) != 2 || got[0] == got[1] {
		t.Errorf("picks after the run = %v, want both proxies: an attempt did not release its proxy", got)
	}
}

func TestStartWorkersBrowserSlots(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)

	t.Run("a full cap holds a create until a holder releases", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var e slotEntries
			releaseFirst := make(chan struct{})
			done := make(chan struct{})
			create := func(_ context.Context, _ *slog.Logger, _ workerState, _ email.Provider, _ proxy.Proxy, slot browserSlot) error {
				if e.enter() == 1 {
					<-releaseFirst
					slot.release()
					slot.release()
				}
				<-done
				return nil
			}
			var g errgroup.Group
			(&runner{cfg: slotConfig(4, 2), proxyMgr: &proxy.Manager{}}).startWorkers(t.Context(), &g, logger, 4, create, runOnce)

			synctest.Wait()
			if n := e.count(); n != 2 {
				t.Errorf("entered with both slots held = %d, want 2", n)
			}
			close(releaseFirst)
			synctest.Wait()
			if n := e.count(); n != 3 {
				t.Errorf("entered after one holder released twice = %d, want 3: a second release frees nothing more", n)
			}
			close(done)
			if err := g.Wait(); err != nil {
				t.Errorf("Wait: %v", err)
			}
			if n := e.count(); n != 4 {
				t.Errorf("entered = %d, want 4", n)
			}
		})
	})

	t.Run("a create that fails without a release frees its slot", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var e slotEntries
			fail := make(chan struct{})
			create := func(context.Context, *slog.Logger, workerState, email.Provider, proxy.Proxy, browserSlot) error {
				if e.enter() == 1 {
					<-fail
					return errors.New("launching chrome: failed")
				}
				return nil
			}
			var g errgroup.Group
			(&runner{cfg: slotConfig(2, 1), proxyMgr: &proxy.Manager{}}).startWorkers(t.Context(), &g, logger, 2, create, runOnce)

			synctest.Wait()
			if n := e.count(); n != 1 {
				t.Errorf("entered with the slot held = %d, want 1", n)
			}
			close(fail)
			if err := g.Wait(); err == nil || err.Error() != "launching chrome: failed" {
				t.Errorf("Wait = %v, want the create error", err)
			}
			if n := e.count(); n != 2 {
				t.Errorf("entered = %d, want 2", n)
			}
		})
	})

	cancelTests := []struct {
		name    string
		start   func(ctx context.Context, g *errgroup.Group, cfg *config.Config, create createFunc)
		wantErr error
	}{
		{
			name: "unlimited",
			start: func(ctx context.Context, g *errgroup.Group, cfg *config.Config, create createFunc) {
				(&runner{cfg: cfg, proxyMgr: &proxy.Manager{}}).startUnlimitedWorkers(ctx, g, logger, create)
			},
		},
		{
			name: "finite",
			start: func(ctx context.Context, g *errgroup.Group, cfg *config.Config, create createFunc) {
				(&runner{cfg: cfg, proxyMgr: &proxy.Manager{}}).startFiniteWorkers(ctx, g, logger, 2, create)
			},
			wantErr: context.Canceled,
		},
	}
	for _, tt := range cancelTests {
		t.Run("a cancel during the wait makes no attempt, "+tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var e slotEntries
				ctx, cancel := context.WithCancel(t.Context())
				g, gCtx := errgroup.WithContext(ctx)
				create := func(ctx context.Context, _ *slog.Logger, _ workerState, _ email.Provider, _ proxy.Proxy, _ browserSlot) error {
					e.enter()
					<-ctx.Done()
					return fmt.Errorf("navigating to registration page: %w", ctx.Err())
				}
				tt.start(gCtx, g, slotConfig(2, 1), create)

				synctest.Wait()
				cancel()
				if err := g.Wait(); !errors.Is(err, tt.wantErr) {
					t.Errorf("Wait = %v, want %v", err, tt.wantErr)
				}
				if n := e.count(); n != 1 {
					t.Errorf("creates = %d, want 1: the waiting worker never enters", n)
				}
			})
		})
	}

	t.Run("the proxy pick comes before the slot wait, and the deadline starts after both waits", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mgr := proxyManager(t, testProxyAddr)
			cooldown, _, _ := mgr.ReportFailure(testProxy())
			start := time.Now()
			var e slotEntries
			var slotStarts []time.Duration
			var proxies []string
			var waited, left time.Duration
			var hasDeadline bool
			// The one slot orders the creates, so they share the slices without a lock.
			create := func(ctx context.Context, _ *slog.Logger, _ workerState, _ email.Provider, p proxy.Proxy, slot browserSlot) error {
				slotStarts = append(slotStarts, slot.waitStart.Sub(start))
				proxies = append(proxies, p.HostPort())
				if e.enter() == 1 {
					time.Sleep(2 * time.Minute)
					return nil
				}
				waited = time.Since(slot.waitStart)
				var deadline time.Time
				deadline, hasDeadline = ctx.Deadline()
				left = time.Until(deadline)
				return nil
			}
			cfg := slotConfig(2, 1)
			var g errgroup.Group
			(&runner{cfg: cfg, proxyMgr: mgr}).startWorkers(t.Context(), &g, logger, 2, create, runOnce)

			if err := g.Wait(); err != nil {
				t.Errorf("Wait: %v", err)
			}
			if got, want := fmt.Sprint(slotStarts), fmt.Sprint([]time.Duration{cooldown, cooldown}); got != want {
				t.Errorf("slot waits start at %s, want %s: each worker waits out the cooldown before the slot", got, want)
			}
			if got, want := fmt.Sprint(proxies), fmt.Sprint([]string{testProxyAddr, testProxyAddr}); got != want {
				t.Errorf("proxies = %s, want %s", got, want)
			}
			if waited != 2*time.Minute {
				t.Errorf("measured slot wait = %v, want exactly 2m0s", waited)
			}
			if !hasDeadline || left != cfg.Concurrency.AccountTimeout {
				t.Errorf("deadline after entry = %v (set %v), want exactly %v", left, hasDeadline, cfg.Concurrency.AccountTimeout)
			}
		})
	})

	t.Run("a cancel during the proxy wait makes no attempt", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mgr := proxyManager(t, testProxyAddr)
			mgr.ReportFailure(testProxy())
			var e slotEntries
			ctx, cancel := context.WithCancel(t.Context())
			create := func(context.Context, *slog.Logger, workerState, email.Provider, proxy.Proxy, browserSlot) error {
				e.enter()
				return nil
			}
			var g errgroup.Group
			(&runner{cfg: slotConfig(1, 1), proxyMgr: mgr}).startWorkers(ctx, &g, logger, 1, create, runOnce)

			synctest.Wait()
			cancel()
			if err := g.Wait(); !errors.Is(err, context.Canceled) || err.Error() != "waiting for a proxy: context canceled" {
				t.Errorf("Wait = %v, want waiting for a proxy: context canceled", err)
			}
			if n := e.count(); n != 0 {
				t.Errorf("creates = %d, want 0", n)
			}
		})
	})
}

func TestStartWorkersReleasesProxy(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)

	t.Run("an attempt holds its proxy until create returns", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mgr := proxyManager(t, testProxyAddr, otherProxyAddr)
			var held string
			var during []string
			create := func(ctx context.Context, _ *slog.Logger, _ workerState, _ email.Provider, p proxy.Proxy, _ browserSlot) error {
				held = p.HostPort()
				during = releasedProxies(ctx, t, mgr, 2)
				return nil
			}
			var g errgroup.Group
			(&runner{cfg: slotConfig(1, 1), proxyMgr: mgr}).startWorkers(t.Context(), &g, logger, 1, create, runOnce)
			if err := g.Wait(); err != nil {
				t.Fatalf("Wait: %v", err)
			}

			other := testProxyAddr
			if held == testProxyAddr {
				other = otherProxyAddr
			}
			if want := []string{other, other}; !slices.Equal(during, want) {
				t.Errorf("picks during create = %v, want %v: the attempt holds %s", during, want, held)
			}
			assertNoLoad(t, mgr)
		})
	})

	t.Run("a failed slot wait releases its proxy", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mgr := proxyManager(t, testProxyAddr, otherProxyAddr)
			ctx, cancel := context.WithCancel(t.Context())
			var e slotEntries
			create := func(ctx context.Context, _ *slog.Logger, _ workerState, _ email.Provider, _ proxy.Proxy, _ browserSlot) error {
				e.enter()
				<-ctx.Done()
				return fmt.Errorf("navigating to registration page: %w", ctx.Err())
			}
			var g errgroup.Group
			(&runner{cfg: slotConfig(2, 1), proxyMgr: mgr}).startWorkers(ctx, &g, logger, 2, create, runOnce)

			// One worker holds the slot, and the other waits for it.
			synctest.Wait()
			cancel()
			if err := g.Wait(); !errors.Is(err, context.Canceled) {
				t.Errorf("Wait = %v, want %v", err, context.Canceled)
			}
			if n := e.count(); n != 1 {
				t.Errorf("creates = %d, want 1", n)
			}
			assertNoLoad(t, mgr)
		})
	})
}

// rowKinds returns "proxy", "slot" or "idle" for each worker row of s, in worker
// order. "slot" is an attempt whose open step is the slot wait.
func rowKinds(t *testing.T, s *runState) []string {
	t.Helper()
	var kinds []string
	for _, w := range s.snapshot().Workers {
		switch {
		case w.Wait != nil && w.Wait.Kind == api.RunWaitProxy:
			kinds = append(kinds, "proxy")
		case w.Attempt != nil && w.Attempt.Step == stepSlotWait && w.Attempt.Proxy != nil && *w.Attempt.Proxy == testProxyAddr:
			kinds = append(kinds, "slot")
		case w.Wait == nil && w.Attempt == nil:
			kinds = append(kinds, "idle")
		default:
			kinds = append(kinds, fmt.Sprintf("%+v", w))
		}
	}
	return kinds
}

func TestStartWorkersRunState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mgr := proxyManager(t, testProxyAddr)
		cooldown, _, _ := mgr.ReportFailure(testProxy())
		state := testRunState(-1)
		ctx, cancel := context.WithCancel(t.Context())
		release := make(chan struct{})
		var e slotEntries
		create := func(context.Context, *slog.Logger, workerState, email.Provider, proxy.Proxy, browserSlot) error {
			e.enter()
			<-release
			return nil
		}
		var g errgroup.Group
		(&runner{cfg: slotConfig(2, 1), proxyMgr: mgr, state: state}).startWorkers(ctx, &g, slog.New(slog.DiscardHandler), 2, create, runOnce)

		synctest.Wait()
		if got := fmt.Sprint(rowKinds(t, state)); got != "[proxy proxy]" {
			t.Errorf("rows during the cooldown = %s, want [proxy proxy]", got)
		}
		// Both workers get the proxy at the cooldown end. One holds the slot, and the other waits for it.
		time.Sleep(cooldown)
		synctest.Wait()
		if got := fmt.Sprint(rowKinds(t, state)); got != "[slot slot]" || e.count() != 1 {
			t.Errorf("rows after the cooldown = %s, creates %d, want [slot slot] and 1", got, e.count())
		}
		// The cancel ends the slot wait, and the worker in create stays in its attempt.
		cancel()
		synctest.Wait()
		if got := rowKinds(t, state); !slices.Contains(got, "idle") || !slices.Contains(got, "slot") {
			t.Errorf("rows after the cancel = %v, want one idle and one in its attempt", got)
		}
		close(release)
		if err := g.Wait(); !errors.Is(err, context.Canceled) {
			t.Errorf("Wait = %v, want context.Canceled", err)
		}
		if got := fmt.Sprint(rowKinds(t, state)); got != "[idle idle]" {
			t.Errorf("rows after the exit = %s, want [idle idle]", got)
		}
	})
}

func TestAttemptAccountMarksBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		state := testRunState(1)
		state.start(1)
		start := time.Now()
		calls := 0
		var second time.Time
		createFn := func(context.Context, *slog.Logger) error {
			calls++
			if calls == 1 {
				return errors.New("failure")
			}
			second = time.Now()
			return nil
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = attemptAccount(t.Context(), slog.New(slog.DiscardHandler), workerState{state: state}, 1, createFn, workerBackoff{initial: time.Minute, max: time.Minute})
		}()

		synctest.Wait()
		wait := state.snapshot().Workers[0].Wait
		<-done

		if wait == nil || wait.Kind != api.RunWaitBackoff || !wait.Since.Equal(start) || wait.Until == nil {
			t.Fatalf("row during the backoff = %+v, want a backoff since the start with an end", wait)
		}
		checkJitter(t, "backoff", wait.Until.Sub(wait.Since), time.Minute)
		if !second.Equal(*wait.Until) {
			t.Errorf("the next attempt started at %v, want the backoff end %v", second, *wait.Until)
		}
	})
}

func TestStartFiniteWorkers(t *testing.T) {
	errCreate := errors.New("create failed")
	tests := []struct {
		name         string
		maxWorkers   int
		accountCount int
		numWorkers   int                     // the workers of the run: the lower of maxWorkers and accountCount
		fail         func(workerN int) error // the result of the call workerN of one worker
		wantCalls    int
		wantErr      string
	}{
		{name: "more accounts than workers", maxWorkers: 2, accountCount: 5, numWorkers: 2, wantCalls: 5},
		{name: "fewer accounts than workers", maxWorkers: 4, accountCount: 1, numWorkers: 1, wantCalls: 1},
		{
			// A worker retries the account of a failed call, so each odd call of a worker is the first call of its account.
			name: "each account that fails once is retried", maxWorkers: 2, accountCount: 5, numWorkers: 2,
			fail: func(workerN int) error {
				if workerN%2 == 1 {
					return errCreate
				}
				return nil
			},
			wantCalls: 10,
		},
		{
			name: "a worker at the limit stops the run", maxWorkers: 1, accountCount: 3, numWorkers: 1,
			fail:      func(int) error { return errCreate },
			wantCalls: maxConsecutiveFailures,
			wantErr:   "worker stopped: account 1: 10 consecutive failures (last: create failed)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cfg := workerConfig(tt.maxWorkers)
				calls := newWorkerCalls()
				g, gCtx := errgroup.WithContext(t.Context())
				after := func(_, workerN int) error {
					if tt.fail == nil {
						return nil
					}
					return tt.fail(workerN)
				}

				r := &runner{cfg: cfg, proxyMgr: &proxy.Manager{}}
				r.startFiniteWorkers(gCtx, g, slog.New(slog.DiscardHandler), tt.accountCount, calls.create(cfg.Concurrency.AccountTimeout, tt.numWorkers, after))

				gotErr := ""
				if err := g.Wait(); err != nil {
					gotErr = err.Error()
				}
				if gotErr != tt.wantErr {
					t.Errorf("Wait = %q, want %q", gotErr, tt.wantErr)
				}
				if calls.n != tt.wantCalls {
					t.Errorf("calls = %d, want %d", calls.n, tt.wantCalls)
				}
				for _, e := range calls.errs {
					t.Error(e)
				}
			})
		})
	}
}

func TestStartUnlimitedWorkers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const stopAfter = 6
		cfg := workerConfig(2)
		calls := newWorkerCalls()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		g, gCtx := errgroup.WithContext(ctx)

		r := &runner{cfg: cfg, proxyMgr: &proxy.Manager{}}
		r.startUnlimitedWorkers(gCtx, g, slog.New(slog.DiscardHandler), calls.create(cfg.Concurrency.AccountTimeout, cfg.Concurrency.MaxWorkers, func(n, _ int) error {
			if n == stopAfter {
				cancel()
			}
			return nil
		}))

		if err := g.Wait(); err != nil {
			t.Fatalf("Wait: %v", err)
		}
		if calls.n < stopAfter {
			t.Errorf("calls = %d, want at least %d", calls.n, stopAfter)
		}
		for _, e := range calls.errs {
			t.Error(e)
		}
	})
}
