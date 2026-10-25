package main

import (
	"slices"
	"testing"
	"testing/synctest"
)

// hubTest runs on a hub with a buffer of 1. Its run returns channels that are
// all closed at its end, and want holds the values of each one in order.
type hubTest struct {
	name string
	run  func(h *hub[int]) []<-chan int
	want [][]int
}

// runHubTests runs each test in a bubble, so a blocked publish or an open
// channel makes synctest.Test panic on the deadlock.
func runHubTests(t *testing.T, tests []hubTest) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHub[int](1)
				chans := tt.run(h)
				if n := len(h.subs); n != 0 {
					t.Errorf("subscribers = %d, want 0", n)
				}
				for i, ch := range chans {
					if got := hubValues(ch); !slices.Equal(got, tt.want[i]) {
						t.Errorf("channel %d values = %v, want %v", i, got, tt.want[i])
					}
				}
			})
		})
	}
}

// hubValues reads ch until it closes.
func hubValues(ch <-chan int) []int {
	got := []int{}
	for v := range ch {
		got = append(got, v)
	}
	return got
}

func TestHubFullBufferDropsSubscriber(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHub[int](3)
		ch, _ := h.subscribe()
		// The subscriber never reads, so a publish that blocks makes synctest.Test panic.
		for v := range 5 {
			h.publish(v)
		}
		if n := len(h.subs); n != 0 {
			t.Errorf("subscribers = %d, want 0", n)
		}
		if got, want := hubValues(ch), []int{0, 1, 2}; !slices.Equal(got, want) {
			t.Errorf("values = %v, want %v", got, want)
		}
	})
}

func TestHubPublish(t *testing.T) {
	runHubTests(t, []hubTest{
		{"two subscribers both get a value", func(h *hub[int]) []<-chan int {
			a, _ := h.subscribe()
			b, _ := h.subscribe()
			h.publish(7)
			h.close()
			return []<-chan int{a, b}
		}, [][]int{{7}, {7}}},
		{"subscribe after close", func(h *hub[int]) []<-chan int {
			h.close()
			ch, _ := h.subscribe()
			return []<-chan int{ch}
		}, [][]int{{}}},
		{"publish after close", func(h *hub[int]) []<-chan int {
			ch, _ := h.subscribe()
			h.close()
			h.publish(1)
			return []<-chan int{ch}
		}, [][]int{{}}},
		{"close twice", func(h *hub[int]) []<-chan int {
			ch, _ := h.subscribe()
			h.close()
			h.close()
			return []<-chan int{ch}
		}, [][]int{{}}},
	})
}

func TestHubCancel(t *testing.T) {
	runHubTests(t, []hubTest{
		{"cancel removes only its subscriber", func(h *hub[int]) []<-chan int {
			a, cancelA := h.subscribe()
			b, _ := h.subscribe()
			cancelA()
			h.publish(1)
			h.close()
			return []<-chan int{a, b}
		}, [][]int{{}, {1}}},
		{"cancel after a drop", func(h *hub[int]) []<-chan int {
			ch, cancel := h.subscribe()
			h.publish(1)
			h.publish(2)
			cancel()
			return []<-chan int{ch}
		}, [][]int{{1}}},
		{"cancel twice", func(h *hub[int]) []<-chan int {
			ch, cancel := h.subscribe()
			cancel()
			cancel()
			return []<-chan int{ch}
		}, [][]int{{}}},
		{"cancel after close", func(h *hub[int]) []<-chan int {
			ch, cancel := h.subscribe()
			h.close()
			cancel()
			return []<-chan int{ch}
		}, [][]int{{}}},
		{"cancel after subscribe after close", func(h *hub[int]) []<-chan int {
			h.close()
			ch, cancel := h.subscribe()
			cancel()
			return []<-chan int{ch}
		}, [][]int{{}}},
	})
}
