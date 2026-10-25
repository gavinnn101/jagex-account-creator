package waittest

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestWaitReturnsWhenTheGoroutinesEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var wg sync.WaitGroup
		wg.Go(func() { time.Sleep(time.Second) })
		start := time.Now()
		Wait(t, &wg)
		if got := time.Since(start); got != time.Second {
			t.Errorf("Wait returned after %v, want 1s", got)
		}
	})
}

// fatalTB records the Fatalf call and ends the goroutine, as testing.T does.
type fatalTB struct {
	testing.TB

	msg string
}

func (*fatalTB) Helper() {}

func (f *fatalTB) Fatalf(format string, args ...any) {
	f.msg = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func TestWaitFailsAtTheGuard(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var wg sync.WaitGroup
		wg.Add(1)
		tb := &fatalTB{TB: t}
		start := time.Now()
		ended := make(chan struct{})
		go func() {
			defer close(ended)
			Wait(tb, &wg)
		}()
		<-ended
		if got := time.Since(start); got != 2*time.Second {
			t.Errorf("Wait failed after %v, want 2s", got)
		}
		if want := "the goroutines did not end within 2s"; tb.msg != want {
			t.Errorf("Fatalf message = %q, want %q", tb.msg, want)
		}
		// The goroutine of Wait ends, so the bubble ends with no blocked goroutine.
		wg.Done()
	})
}
