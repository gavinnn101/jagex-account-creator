package plain

import "sync"

// Block is in a package whose import path does not end in "test", so no scan reads it.
func Block(wg *sync.WaitGroup) {
	wg.Wait()
}
