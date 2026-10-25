package bubbled

import (
	"sync"
	"testing"
	"testing/synctest"

	"fixture/drainertest"
)

// The dir sorts before each unbubbled caller of Drain, so a scan that reads a helper only once misses Drain.
func TestA(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var wg sync.WaitGroup
		drainertest.Drain(&wg)
		drainertest.Settle(&wg)
	})
}
