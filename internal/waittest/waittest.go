// Package waittest waits for the goroutines of a test under a guard.
package waittest

import (
	"sync"
	"testing"
	"time"
)

// guard is at least 2s and below the per-mutant timeout of the mutation gate.
const guard = 2 * time.Second

// Wait waits for wg. It fails the test when the goroutines do not end within the guard.
// Thus a goroutine that hangs fails the test and does not hang it.
func Wait(tb testing.TB, wg *sync.WaitGroup) {
	tb.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(guard):
		tb.Fatalf("the goroutines did not end within %v", guard)
	}
}
