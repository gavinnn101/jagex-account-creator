package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"time"

	"jagex_account_creator/internal/api"
)

// replayBuffer is the write buffer of a replay, so a long replay costs one write for each 64 KiB.
const replayBuffer = 64 << 10

// StreamRunEvents sends the run state, then each later state, until the client
// goes or serve stops.
func (s *apiServer) StreamRunEvents(ctx context.Context, _ api.StreamRunEventsRequestObject) (api.StreamRunEventsResponseObject, error) {
	first, values, cancel := s.subscribeRun()
	return sseStream[api.RunState]{backlog: backlogOf(first), values: values, cancel: cancel, done: ctx.Done()}, nil
}

// StreamLogs sends the lines of the current or last run of serve, then each new
// line, until the client goes or serve stops. The subscription and the last
// published line come under one lock, so the replay ends where the live lines
// start. A Last-Event-ID of that run starts the replay after it.
func (s *apiServer) StreamLogs(ctx context.Context, r api.StreamLogsRequestObject) (api.StreamLogsResponseObject, error) {
	last, values, cancel := s.logs.subscribeLast()
	stream := sseStream[logLine]{values: values, id: logLine.eventID, cancel: cancel, done: ctx.Done()}
	if last != nil {
		from := replayStart(r.Params.LastEventID, last.RunId, last.end)
		stream.backlog = replayLog(runLogPath(s.config().Database, last.RunId), last.RunId, from, last.end)
	}
	return stream, nil
}

// subscribeRun returns the run state and a subscription to each later state.
func (s *apiServer) subscribeRun() (api.RunState, <-chan api.RunState, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil {
		// StartRun publishes the first state of a run under s.mu, so this subscription sees it.
		ch, cancel := s.events.subscribe()
		return s.state.snapshot(), ch, cancel
	}
	return s.state.subscribe()
}

// closeStreams closes both hubs, so each open stream ends and a later one ends at once.
func (s *apiServer) closeStreams() {
	s.events.close()
	s.logs.close()
}

// backlogOf returns a backlog of the one value v.
func backlogOf[T any](v T) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) { yield(v, nil) }
}

// sseStream is the answer to a stream request. It sends each value of backlog,
// then each value of values, as server-sent events. It ends when values closes
// or done closes, and then calls cancel. An error of backlog also ends it.
type sseStream[T any] struct {
	backlog iter.Seq2[T, error] // nil sends no backlog
	values  <-chan T
	id      func(T) string // the event id of a value. nil sends no id
	cancel  func()
	done    <-chan struct{}
}

// VisitStreamRunEventsResponse writes the stream to w.
func (s sseStream[T]) VisitStreamRunEventsResponse(w http.ResponseWriter) error {
	s.write(w)
	return nil
}

// VisitStreamLogsResponse writes the stream to w.
func (s sseStream[T]) VisitStreamLogsResponse(w http.ResponseWriter) error {
	s.write(w)
	return nil
}

// write sends the stream to w. Each error ends the stream with no answer,
// because the status is already sent.
func (s sseStream[T]) write(w http.ResponseWriter) {
	defer s.cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	// A client opens the stream when the headers arrive, and the log stream can have no backlog.
	if rc.Flush() != nil {
		return
	}
	if s.backlog != nil && !s.sendBacklog(w, rc) {
		return
	}
	// Limitation: a half-open TCP client on a quiet stream holds its handler until the next write fails or serve stops.
	for {
		select {
		case <-s.done:
			return
		case v, ok := <-s.values:
			if !ok {
				return
			}
			if !s.writeEvent(deadlineWriter{w, rc}, v) || rc.Flush() != nil {
				return
			}
		}
	}
}

// sendBacklog writes each value of the backlog through one buffer and flushes
// once at the end, so a long replay costs no flush for each event. It reports
// whether each step succeeded.
func (s sseStream[T]) sendBacklog(w http.ResponseWriter, rc *http.ResponseController) bool {
	bw := bufio.NewWriterSize(deadlineWriter{w, rc}, replayBuffer)
	// Limitation: a replay error, such as a deleted log file of the last run, ends the stream after the headers with no log line.
	// The client then connects again in a loop until the next run.
	for v, err := range s.backlog {
		if err != nil || !s.writeEvent(bw, v) {
			return false
		}
	}
	return bw.Flush() == nil && rc.Flush() == nil
}

// writeEvent writes v as one event to w in one write, with its id when s has
// an id func, and reports whether it succeeded.
func (s sseStream[T]) writeEvent(w io.Writer, v T) bool {
	data, err := json.Marshal(v)
	if err != nil {
		return false
	}
	if s.id != nil {
		_, err = fmt.Fprintf(w, "id: %s\ndata: %s\n\n", s.id(v), data)
	} else {
		_, err = fmt.Fprintf(w, "data: %s\n\n", data)
	}
	return err == nil
}

// deadlineWriter moves the write deadline of an answer before each write, so a
// long answer outlives the server's write timeout and a stalled client still
// ends it.
type deadlineWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

// Write moves the deadline, then writes p.
func (d deadlineWriter) Write(p []byte) (int, error) {
	if err := d.rc.SetWriteDeadline(time.Now().Add(apiWriteTimeout)); err != nil {
		return 0, fmt.Errorf("moving the write deadline: %w", err)
	}
	n, err := d.w.Write(p)
	if err != nil {
		return n, fmt.Errorf("writing the answer: %w", err)
	}
	return n, nil
}
