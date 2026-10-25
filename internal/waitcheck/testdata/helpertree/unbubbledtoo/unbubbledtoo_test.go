package unbubbledtoo

import (
	"sync"
	"testing"

	"fixture/drainertest"
)

// A second unbubbled caller of Drain: its wait still reports once.
func TestA(t *testing.T) {
	var wg sync.WaitGroup
	drainertest.Drain(&wg)
}
