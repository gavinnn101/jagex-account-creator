package browser

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func TestPumpRecordsUntilRecvFails(t *testing.T) {
	events := []int{1, 2, 3}
	recv := func() (int, error) {
		if len(events) == 0 {
			return 0, errors.New("stream closed")
		}
		e := events[0]
		events = events[1:]
		return e, nil
	}
	var got []int
	closes := 0

	pump(func() error { closes++; return errors.New("ignored") }, recv, func(e int) { got = append(got, e) })

	if !slices.Equal(got, []int{1, 2, 3}) {
		t.Errorf("recorded %v, want [1 2 3]", got)
	}
	if closes != 1 {
		t.Errorf("closeStream called %d times, want 1", closes)
	}
}

// fakeSyncSet stands in for a cdp.Sync set. One shared queue makes one stream
// ready at a time, in the order of the queue.
type fakeSyncSet struct {
	queue []fakeEvent
}

type fakeEvent struct {
	stream int
	value  string
}

type fakeStream struct {
	set    *fakeSyncSet
	index  int
	closes int
}

// Ready is closed when the next event is this stream's, or when the set is
// drained, as a closed stream's Ready is.
func (s *fakeStream) Ready() <-chan struct{} {
	if len(s.set.queue) > 0 && s.set.queue[0].stream != s.index {
		return make(chan struct{})
	}
	c := make(chan struct{})
	close(c)
	return c
}

func (s *fakeStream) Recv() (string, error) {
	if len(s.set.queue) == 0 {
		return "", errors.New("stream closed")
	}
	event := s.set.queue[0]
	s.set.queue = s.set.queue[1:]
	return event.value, nil
}

func (s *fakeStream) Close() error {
	s.closes++
	return errors.New("ignored")
}

func TestPumpSyncedRecordsInQueueOrder(t *testing.T) {
	set := &fakeSyncSet{queue: []fakeEvent{{0, "request"}, {1, "response"}, {1, "response 2"}, {2, "finish"}, {0, "request 2"}}}
	streams := []*fakeStream{{set: set, index: 0}, {set: set, index: 1}, {set: set, index: 2}}
	var got []string
	record := func(name string) func(string) {
		return func(v string) { got = append(got, name+": "+v) }
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		pumpSynced([]syncedStream{
			synced(streams[0], record("a")),
			synced(streams[1], record("b")),
			synced(streams[2], record("c")),
		})
	}()
	// A pump that does not stop on the failed receive spins, so it fails here.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pumpSynced did not return after a failed receive")
	}

	want := []string{"a: request", "b: response", "b: response 2", "c: finish", "a: request 2"}
	if !slices.Equal(got, want) {
		t.Errorf("recorded %q, want %q", got, want)
	}
	for i, s := range streams {
		if s.closes != 1 {
			t.Errorf("stream %d closed %d times, want 1", i, s.closes)
		}
	}
}
