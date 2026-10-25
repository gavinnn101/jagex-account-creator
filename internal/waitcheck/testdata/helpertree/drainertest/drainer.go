package drainertest

import (
	"sync"

	"fixture/closertest"
)

// Drain runs in a bubble in one caller and outside a bubble in two others.
func Drain(wg *sync.WaitGroup) {
	wg.Wait() // want wait
}

// Settle runs only in a bubble.
func Settle(wg *sync.WaitGroup) {
	wg.Wait()
}

// Stop reaches a wait of another helper package.
func Stop(wg *sync.WaitGroup) {
	closertest.Close(wg)
}
