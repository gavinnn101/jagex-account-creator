package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"jagex_account_creator/internal/api"
	"jagex_account_creator/internal/logtest"
)

// streamWriter is the response writer of a stream on a live connection: a
// ResponseController can flush it and set its write deadline. The stream
// writes from its own goroutine, so one mutex guards the fields.
type streamWriter struct {
	mu           sync.Mutex
	header       http.Header
	sent         written
	flushLimit   int // the flushes that pass. A negative limit passes each one
	failDeadline bool
	failWrite    bool
}

// written is what a streamWriter got.
type written struct {
	status   int
	header   http.Header // the header at WriteHeader
	body     string
	flushed  []int // the body length at each flush
	deadline time.Time
}

func newStreamWriter() *streamWriter {
	return &streamWriter{header: http.Header{}, flushLimit: -1}
}

func (w *streamWriter) Header() http.Header { return w.header }

func (w *streamWriter) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sent.status, w.sent.header = code, w.header.Clone()
}

func (w *streamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failWrite {
		return 0, errors.New("client gone")
	}
	w.sent.body += string(p)
	return len(p), nil
}

func (w *streamWriter) FlushError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.sent.flushed) == w.flushLimit {
		return errors.New("client gone")
	}
	w.sent.flushed = append(w.sent.flushed, len(w.sent.body))
	return nil
}

func (w *streamWriter) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failDeadline {
		return errors.New("connection closed")
	}
	w.sent.deadline = t
	return nil
}

// read returns what w got so far.
func (w *streamWriter) read() written {
	w.mu.Lock()
	defer w.mu.Unlock()
	got := w.sent
	got.flushed = append([]int(nil), w.sent.flushed...)
	return got
}

// visit runs the Visit method fn on w in its own goroutine. The returned
// channel gets the result of fn when the stream ends.
func visit(w *streamWriter, fn func(http.ResponseWriter) error) <-chan error {
	result := make(chan error, 1)
	go func() { result <- fn(w) }()
	return result
}

// assertEnded fails the test when the stream has not ended with a nil result.
// The caller has called synctest.Wait.
func assertEnded(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		if err != nil {
			t.Errorf("Visit() = %v, want nil once the headers are sent", err)
		}
	default:
		t.Error("the stream did not end")
	}
}

// eventOf returns the event of v on the wire.
func eventOf(t *testing.T, v any) string {
	t.Helper()
	return "data: " + jsonOf(t, v) + "\n\n"
}

// subscribers returns the number of subscribers of h.
func subscribers[T any](h *hub[T]) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// streamRun opens the run state stream of s on w, with a request context that
// cancel ends.
func streamRun(t *testing.T, s *apiServer, w *streamWriter) (result <-chan error, cancel context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	resp, err := s.StreamRunEvents(ctx, api.StreamRunEventsRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	return visit(w, resp.VisitStreamRunEventsResponse), cancel
}

func TestStreamRunEvents(t *testing.T) {
	t.Run("the first state, then each mark, each with a new write deadline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newServeAPI(io.Discard)
			s.state = newRunState(1, s.events)
			w := newStreamWriter()
			_, cancel := streamRun(t, s, w)
			defer cancel()
			first := s.state.snapshot()
			synctest.Wait()

			got := w.read()
			if got.body != eventOf(t, first) || len(got.flushed) != 2 || got.flushed[0] != 0 {
				t.Errorf("body %q, flushed at %v, want the first state after a flush of the headers", got.body, got.flushed)
			}
			if !got.deadline.Equal(time.Now().Add(apiWriteTimeout)) {
				t.Errorf("deadline = %v, want now plus %v", got.deadline, apiWriteTimeout)
			}
			if ct := got.header.Get("Content-Type"); ct != "text/event-stream" || got.status != http.StatusOK {
				t.Errorf("status %d, Content-Type %q, want 200 text/event-stream", got.status, ct)
			}
			if cc := got.header.Get("Cache-Control"); cc != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", cc)
			}

			time.Sleep(time.Minute)
			s.state.start(1)
			synctest.Wait()

			got = w.read()
			if want := eventOf(t, first) + eventOf(t, s.state.snapshot()); got.body != want {
				t.Errorf("body =\n%q\nwant\n%q", got.body, want)
			}
			if !got.deadline.Equal(time.Now().Add(apiWriteTimeout)) {
				t.Errorf("deadline = %v, want the time of the second event plus %v", got.deadline, apiWriteTimeout)
			}
		})
	})

	t.Run("the end of the request ends the stream and its subscription", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newServeAPI(io.Discard)
			result, cancel := streamRun(t, s, newStreamWriter())
			synctest.Wait()

			cancel()
			synctest.Wait()

			assertEnded(t, result)
			if n := subscribers(s.events); n != 0 {
				t.Errorf("the hub has %d subscribers after the end, want 0", n)
			}
		})
	})

	t.Run("the hub close ends the stream", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newServeAPI(io.Discard)
			w := newStreamWriter()
			result, cancel := streamRun(t, s, w)
			defer cancel()
			synctest.Wait()

			s.closeStreams()
			synctest.Wait()

			assertEnded(t, result)
			if body := w.read().body; body != eventOf(t, s.state.snapshot()) {
				t.Errorf("body = %q, want only the idle state", body)
			}
		})
	})
}

// Each failed step ends the stream, so no later event reaches the writer.
func TestStreamFailingWriter(t *testing.T) {
	tests := []struct {
		name   string
		fail   func(w *streamWriter)
		events int // the events that reached the writer
	}{
		{"the header flush fails", func(w *streamWriter) { w.flushLimit = 0 }, 0},
		{"the deadline fails", func(w *streamWriter) { w.failDeadline = true }, 0},
		{"the write fails", func(w *streamWriter) { w.failWrite = true }, 0},
		{"the flush of the first event fails", func(w *streamWriter) { w.flushLimit = 1 }, 1},
		{"the flush of a later event fails", func(w *streamWriter) { w.flushLimit = 2 }, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := newServeAPI(io.Discard)
				w := newStreamWriter()
				tt.fail(w)

				result, cancel := streamRun(t, s, w)
				defer cancel()
				synctest.Wait()
				s.events.publish(s.state.snapshot())
				synctest.Wait()

				assertEnded(t, result)
				want := strings.Repeat(eventOf(t, s.state.snapshot()), tt.events)
				if body := w.read().body; body != want {
					t.Errorf("body = %q, want %q", body, want)
				}
				if n := subscribers(s.events); n != 0 {
					t.Errorf("the hub has %d subscribers after the end, want 0", n)
				}
			})
		})
	}
}

// A value that does not encode ends the stream with no event.
func TestStreamValueDoesNotEncode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newStreamWriter()
		stream := sseStream[func()]{backlog: backlogOf(func() {}), cancel: func() {}}

		result := visit(w, stream.VisitStreamRunEventsResponse)
		synctest.Wait()

		assertEnded(t, result)
		if body := w.read().body; body != "" {
			t.Errorf("body = %q, want nothing", body)
		}
	})
}

// newLogAPI returns an apiServer whose database is in a temp dir, with a log
// hub whose subscribers each buffer size lines.
func newLogAPI(t *testing.T, size int) *apiServer {
	t.Helper()
	s := newServeAPI(io.Discard)
	s.cfg = tempConfig(t, "")
	s.logs = newHub[logLine](size)
	return s
}

// openServeRunLog opens the log file of run runID of s on the log hub of s,
// and closes it at the end of the test.
func openServeRunLog(t *testing.T, s *apiServer, runID int64) (*runLog, *slog.Logger) {
	t.Helper()
	l, logger, err := openRunLog(s.cfg.Database, runID, runLogOptions{base: logtest.New(io.Discard).Handler(), level: slog.LevelInfo, logs: s.logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, logger
}

// streamLogs opens the log stream of s on w with the Last-Event-ID lastID.
func streamLogs(t *testing.T, s *apiServer, w *streamWriter, lastID *string) (result <-chan error, cancel context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	resp, err := s.StreamLogs(ctx, api.StreamLogsRequestObject{Params: api.StreamLogsParams{LastEventID: lastID}})
	if err != nil {
		t.Fatal(err)
	}
	return visit(w, resp.VisitStreamLogsResponse), cancel
}

// fileLines returns the lines of the log file of run runID of s, each with its end offset.
func fileLines(t *testing.T, s *apiServer, runID int64) []logLine {
	t.Helper()
	var lines []logLine
	end := int64(0)
	for text := range strings.SplitAfterSeq(readRunLog(t, s.cfg, runID), "\n") {
		if text == "" {
			continue
		}
		end += int64(len(text))
		lines = append(lines, logLine{LogLine: parseLogLine(runID, strings.TrimSuffix(text, "\n")), end: end})
	}
	return lines
}

// logEvents returns the events of lines on the wire.
func logEvents(t *testing.T, lines ...logLine) string {
	t.Helper()
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("id: " + l.eventID() + "\n" + eventOf(t, l.LogLine))
	}
	return b.String()
}

func TestStreamLogs(t *testing.T) {
	t.Run("with no run the headers go out alone, then each line of a new run with its id", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newLogAPI(t, logLineBuffer)
			w := newStreamWriter()
			result, cancel := streamLogs(t, s, w, nil)
			defer cancel()
			synctest.Wait()

			if got := w.read(); got.body != "" || len(got.flushed) != 1 || got.header.Get("Content-Type") != "text/event-stream" {
				t.Errorf("got %+v, want one flush of the event stream headers and no event", got)
			}

			_, logger := openServeRunLog(t, s, 1)
			logger.Info("two\nlines", "k", "v")
			synctest.Wait()

			lines := fileLines(t, s, 1)
			if got := w.read(); got.body != logEvents(t, lines...) || len(got.flushed) != 2 || !strings.HasPrefix(got.body, "id: 1:"+strconv.Itoa(int(lines[0].end))+"\ndata: {") {
				t.Errorf("body = %q, flushed %v, want the one event with its id and a flush", got.body, got.flushed)
			}
			if deadline := w.read().deadline; !deadline.Equal(time.Now().Add(apiWriteTimeout)) {
				t.Errorf("deadline = %v, want now plus %v", deadline, apiWriteTimeout)
			}
			s.closeStreams()
			synctest.Wait()
			assertEnded(t, result)
		})
	})

	t.Run("the replay of the run comes in one flush, then each live line in its own", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newLogAPI(t, logLineBuffer)
			_, logger := openServeRunLog(t, s, 1)
			for i := range 3 {
				logger.Info("before", "i", i)
			}
			w := newStreamWriter()
			_, cancel := streamLogs(t, s, w, nil)
			defer cancel()
			synctest.Wait()

			replay := logEvents(t, fileLines(t, s, 1)...)
			if got := w.read(); got.body != replay || len(got.flushed) != 2 || got.flushed[1] != len(replay) {
				t.Errorf("body = %q, flushed %v, want the three lines in one flush after the headers", got.body, got.flushed)
			}

			logger.Info("live")
			synctest.Wait()

			lines := fileLines(t, s, 1)
			if got := w.read(); got.body != replay+logEvents(t, lines[3]) || len(got.flushed) != 3 {
				t.Errorf("body = %q, flushed %v, want the replay, then the live line in its own flush", got.body, got.flushed)
			}
		})
	})

	t.Run("a Last-Event-ID of the run resumes after it, and any other id replays the run", func(t *testing.T) {
		tests := []struct {
			name   string
			lastID func(lines []logLine) string
			from   int // the first line that the stream sends
		}{
			{"the id of the first line", func(l []logLine) string { return l[0].eventID() }, 1},
			{"the id of the last line", func(l []logLine) string { return l[2].eventID() }, 3},
			{"an id of another run", func(l []logLine) string { return "2:" + strconv.FormatInt(l[0].end, 10) }, 0},
			{"an id that does not parse", func([]logLine) string { return "garbage" }, 0},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					s := newLogAPI(t, logLineBuffer)
					_, logger := openServeRunLog(t, s, 1)
					for i := range 3 {
						logger.Info("line", "i", i)
					}
					lines := fileLines(t, s, 1)
					w := newStreamWriter()
					_, cancel := streamLogs(t, s, w, new(tt.lastID(lines)))
					defer cancel()
					synctest.Wait()

					if body := w.read().body; body != logEvents(t, lines[tt.from:]...) {
						t.Errorf("body =\n%s\nwant the lines from %d", body, tt.from)
					}
				})
			})
		}
	})

	t.Run("a client that the hub drops gets each line once after its reconnect", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newLogAPI(t, 2)
			_, logger := openServeRunLog(t, s, 1)
			logger.Info("replayed")
			// The stream subscribes here, and its writes start only at the visit.
			resp, err := s.StreamLogs(t.Context(), api.StreamLogsRequestObject{})
			if err != nil {
				t.Fatal(err)
			}
			for i := range 4 {
				logger.Info("burst", "i", i)
			}
			if n := subscribers(s.logs); n != 0 {
				t.Fatalf("the hub has %d subscribers after the burst, want 0", n)
			}
			first := newStreamWriter()
			result := visit(first, resp.VisitStreamLogsResponse)
			synctest.Wait()
			assertEnded(t, result)

			lines := fileLines(t, s, 1)
			// The replay and the two buffered lines reach the client, then the stream ends.
			if body := first.read().body; body != logEvents(t, lines[:3]...) {
				t.Fatalf("first body =\n%s\nwant the replay and the two buffered lines", body)
			}
			second := newStreamWriter()
			_, cancel := streamLogs(t, s, second, new(lines[2].eventID()))
			defer cancel()
			synctest.Wait()

			if body := second.read().body; body != logEvents(t, lines[3:]...) {
				t.Errorf("second body =\n%s\nwant the two lines after the last id", body)
			}
		})
	})

	t.Run("a new run on an open stream sends each of its lines, also below the size of the last run", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newLogAPI(t, logLineBuffer)
			l1, logger1 := openServeRunLog(t, s, 1)
			logger1.Info("a long line of the first run", "pad", strings.Repeat("x", 200))
			w := newStreamWriter()
			_, cancel := streamLogs(t, s, w, nil)
			defer cancel()
			synctest.Wait()
			if err := l1.Close(); err != nil {
				t.Fatal(err)
			}

			_, logger2 := openServeRunLog(t, s, 2)
			logger2.Info("short")
			logger2.Info("short again")
			synctest.Wait()

			run2 := fileLines(t, s, 2)
			if run2[1].end >= fileLines(t, s, 1)[0].end {
				t.Fatalf("run 2 ends at %d, want below the end of run 1", run2[1].end)
			}
			if body := w.read().body; body != logEvents(t, fileLines(t, s, 1)...)+logEvents(t, run2...) {
				t.Errorf("body =\n%s\nwant the line of run 1, then both lines of run 2", body)
			}
		})
	})

	t.Run("a replay that fails ends the stream after the headers", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newLogAPI(t, logLineBuffer)
			// A published line whose file does not exist.
			s.logs.publish(logLine{LogLine: parseLogLine(9, "a=1"), end: 4})
			w := newStreamWriter()
			result, cancel := streamLogs(t, s, w, nil)
			defer cancel()
			synctest.Wait()

			assertEnded(t, result)
			if got := w.read(); got.body != "" || len(got.flushed) != 1 {
				t.Errorf("got %+v, want the headers and no event", got)
			}
			if n := subscribers(s.logs); n != 0 {
				t.Errorf("the hub has %d subscribers after the end, want 0", n)
			}
		})
	})
}
