package closertest

import "sync"

// Close is read only through the import in drainertest.
func Close(wg *sync.WaitGroup) {
	wg.Wait() // want wait
}
