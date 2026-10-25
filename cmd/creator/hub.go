package main

import "sync"

// runEventBuffer holds about one attempt of each of 10 workers. An attempt makes
// 18 step marks and a few row marks, and each mark publishes one state.
const runEventBuffer = 256

// logLineBuffer holds a DEBUG burst of about 100 lines from each of 10 workers.
const logLineBuffer = 1024

// hub sends each published value to every subscriber. A publish never blocks. The
// hub closes and removes a subscriber whose buffer is full, so its stream ends.
type hub[T any] struct {
	mu     sync.Mutex
	size   int
	subs   map[chan T]struct{}
	closed bool
	last   *T // the last published value, nil before the first publish
}

// newHub returns an open hub whose subscribers each buffer size values.
func newHub[T any](size int) *hub[T] {
	return &hub[T]{size: size, subs: map[chan T]struct{}{}}
}

// publish sends v to each subscriber, and closes and removes each one whose buffer is full.
func (h *hub[T]) publish(v T) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.last = &v
	for ch := range h.subs {
		select {
		case ch <- v:
		default:
			h.drop(ch)
		}
	}
}

// subscribe returns a new subscriber channel and the func that cancels it.
// On a closed hub the channel is already closed, so a late stream ends at once.
func (h *hub[T]) subscribe() (<-chan T, func()) {
	_, ch, cancel := h.subscribeLast()
	return ch, cancel
}

// subscribeLast is subscribe that also returns the last published value, or
// nil when the hub has published none. The channel gets each value after last.
func (h *hub[T]) subscribeLast() (last *T, ch <-chan T, cancel func()) {
	sub := make(chan T, h.size)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		close(sub)
	} else {
		h.subs[sub] = struct{}{}
	}
	return h.last, sub, func() { h.cancel(sub) }
}

// cancel closes and removes ch. A channel that the hub already removed stays as it is.
func (h *hub[T]) cancel(ch chan T) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[ch]; ok {
		h.drop(ch)
	}
}

// drop removes ch and closes it. The caller holds h.mu.
func (h *hub[T]) drop(ch chan T) {
	delete(h.subs, ch)
	close(ch)
}

// close closes and removes every subscriber. A later subscribe gets a closed channel.
func (h *hub[T]) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for ch := range h.subs {
		h.drop(ch)
	}
}
