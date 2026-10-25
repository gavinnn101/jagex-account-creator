package unbubbled

import (
	"sync"
	"testing"

	"fixture/drainertest"
	"fixture/plain"
)

func TestA(t *testing.T) {
	var wg sync.WaitGroup
	drainertest.Drain(&wg)
	drainertest.Stop(&wg)
	plain.Block(&wg)
}
