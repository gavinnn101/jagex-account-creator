package browser

import "reflect"

// eventStream is the part of a cdp event client that synced uses.
type eventStream[E any] interface {
	Ready() <-chan struct{}
	Recv() (E, error)
	Close() error
}

// syncedStream is one stream of a cdp.Sync set. recv receives its ready event
// and records it. On a failed receive, recv returns the Recv error as it is.
type syncedStream struct {
	ready func() <-chan struct{}
	recv  func() error
	close func() error
}

// synced returns the syncedStream that passes each event of s to record.
func synced[E any](s eventStream[E], record func(E)) syncedStream {
	return syncedStream{
		ready: s.Ready,
		recv: func() error {
			event, err := s.Recv()
			if err != nil {
				return err
			}
			record(event)
			return nil
		},
		close: s.Close,
	}
}

// pumpSynced records the events of streams until a receive fails. Then it
// closes every stream. cdp.Sync makes one stream ready at a time. One goroutine
// records each event before the next. So the record order is the order that
// the target sent.
// Limitation: both pumps stop silently. A stop that is no Close and no ctx end (a decode
// error, a Chrome exit) leaves no trace. The clearance gate then fails toward ErrChallengeTimeout.
func pumpSynced(streams []syncedStream) {
	defer func() {
		for _, s := range streams {
			_ = s.close()
		}
	}()
	cases := make([]reflect.SelectCase, len(streams))
	for {
		// Ready returns a new channel after each drained event.
		for i, s := range streams {
			cases[i] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(s.ready())}
		}
		chosen, _, _ := reflect.Select(cases)
		if err := streams[chosen].recv(); err != nil {
			return
		}
	}
}

// pump passes each event from recv to record until recv fails. Then it closes
// the stream.
func pump[E any](closeStream func() error, recv func() (E, error), record func(E)) {
	defer func() { _ = closeStream() }()
	for {
		event, err := recv()
		if err != nil {
			return
		}
		record(event)
	}
}
