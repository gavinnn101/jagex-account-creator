package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"jagex_account_creator/internal/api"
)

// logLine is one line of a run log on the log stream: its API form, and the
// byte offset after the line in the log file. Its JSON holds only the API form.
type logLine struct {
	api.LogLine

	end int64
}

// eventID returns the server-sent event id of l, <run_id>:<end>.
func (l logLine) eventID() string {
	return strconv.FormatInt(l.RunId, 10) + ":" + strconv.FormatInt(l.end, 10)
}

// runLogOptions are the outputs of a run logger beside its file: the handler
// of the caller, the level of both, and the log stream, which is nil in the CLI.
type runLogOptions struct {
	base  slog.Handler
	level slog.Leveler
	logs  *hub[logLine]
}

// runLog is the log file of one run. Each Write appends one line and publishes
// it under one lock, so the stream gets the lines in the order of the file.
type runLog struct {
	mu    sync.Mutex
	f     *os.File
	runID int64
	end   int64         // the size of the file
	logs  *hub[logLine] // nil in the CLI
}

// runLogPath returns the path of the log file of run runID, in the logs dir
// beside database.
func runLogPath(database string, runID int64) string {
	return filepath.Join(filepath.Dir(database), "logs", "run-"+strconv.FormatInt(runID, 10)+".log")
}

// openRunLog creates the log file of run runID beside database and returns it
// with the run logger. The run logger sends each line to opts.base, and as
// slog text at opts.level to the file and opts.logs.
func openRunLog(database string, runID int64, opts runLogOptions) (*runLog, *slog.Logger, error) {
	path := runLogPath(database, runID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, fmt.Errorf("creating the run log dir: %w", err)
	}
	// O_EXCL keeps the log of another run, so a run never appends to an old file.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			err = fmt.Errorf("%w (a logs dir kept from a deleted database holds this file)", err)
		}
		return nil, nil, fmt.Errorf("creating the run log: %w", err)
	}
	l := &runLog{f: f, runID: runID, logs: opts.logs}
	return l, slog.New(slog.NewMultiHandler(opts.base, newTextHandler(l, opts.level))), nil
}

// Write appends one line to the file and publishes its API form with its end
// offset. slog writes one line in each call and reuses its buffer after the
// call, so the published form holds a copy.
func (l *runLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.f.Write(p)
	l.end += int64(n)
	if err != nil {
		return n, fmt.Errorf("appending to the run log: %w", err)
	}
	if l.logs != nil {
		l.logs.publish(logLine{LogLine: parseLogLine(l.runID, string(bytes.TrimSuffix(p, []byte("\n")))), end: l.end})
	}
	return n, nil
}

// Close closes the file. A later Write fails.
func (l *runLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.f.Close(); err != nil {
		return fmt.Errorf("closing the run log: %w", err)
	}
	return nil
}

// replayStart returns the offset after the event id lastID when it names run
// runID at or below end. Any other id, or no id, starts at 0.
func replayStart(lastID *string, runID, end int64) int64 {
	if lastID == nil {
		return 0
	}
	run, at, ok := strings.Cut(*lastID, ":")
	if !ok {
		return 0
	}
	id, err := strconv.ParseInt(run, 10, 64)
	if err != nil || id != runID {
		return 0
	}
	offset, err := strconv.ParseInt(at, 10, 64)
	if err != nil || offset < 0 || offset > end {
		return 0
	}
	return offset
}

// replayLog returns the lines of the log file at path, of run runID, from the
// byte offset from up to the byte offset end. end is the end of a line that the
// run log published, so the file holds every line up to it.
func replayLog(path string, runID, from, end int64) iter.Seq2[logLine, error] {
	return func(yield func(logLine, error) bool) {
		f, err := os.Open(path)
		if err != nil {
			yield(logLine{}, fmt.Errorf("replaying the run log: %w", err))
			return
		}
		defer func() { _ = f.Close() }()
		r := bufio.NewReader(io.NewSectionReader(f, from, end-from))
		at := from
		for {
			text, err := r.ReadString('\n')
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				yield(logLine{}, fmt.Errorf("replaying the run log: %w", err))
				return
			}
			at += int64(len(text))
			if !yield(logLine{LogLine: parseLogLine(runID, strings.TrimSuffix(text, "\n")), end: at}, nil) {
				return
			}
		}
	}
}

// parseLogLine returns the API form of text, one line of slog text of run
// runID. A line that does not split keeps its whole text as msg.
func parseLogLine(runID int64, text string) api.LogLine {
	out := api.LogLine{RunId: runID, Attrs: []api.LogAttr{}}
	attrs, ok := splitLogLine(text)
	if !ok {
		out.Msg = text
		return out
	}
	// The text handler writes time, level and msg first. A record with no time has no time key.
	for _, f := range []struct {
		key string
		dst *string
	}{{"time", &out.Time}, {"level", &out.Level}, {"msg", &out.Msg}} {
		if len(attrs) > 0 && attrs[0].Key == f.key {
			*f.dst = attrs[0].Value
			attrs = attrs[1:]
		}
	}
	out.Attrs = attrs
	return out
}

// splitLogLine returns the key-value pairs of text, one line of slog text, in
// order, and false when text does not split.
func splitLogLine(text string) ([]api.LogAttr, bool) {
	attrs := []api.LogAttr{}
	for text != "" {
		key, rest, ok := logToken(text, '=')
		if !ok {
			return nil, false
		}
		value, rest, ok := logToken(rest, ' ')
		if !ok {
			return nil, false
		}
		attrs = append(attrs, api.LogAttr{Key: key, Value: value})
		text = rest
	}
	return attrs, true
}

// logToken returns the key or the value at the start of s and the text after
// its separator sep. The text handler quotes a token with strconv.Quote when it
// holds a space, '=', '"' or a byte that is not printable. So an unquoted key
// ends at '=', and an unquoted value ends at ' ' or at the end of the line.
func logToken(s string, sep byte) (token, rest string, ok bool) {
	if strings.HasPrefix(s, `"`) {
		q, err := strconv.QuotedPrefix(s)
		if err != nil {
			return "", "", false
		}
		// QuotedPrefix returns only a string that Unquote reads.
		token, _ = strconv.Unquote(q)
		rest = s[len(q):]
	} else if i := strings.IndexByte(s, sep); i >= 0 {
		token, rest = s[:i], s[i:]
	} else {
		token = s
	}
	switch {
	case rest == "":
		// Only a value can end the line.
		return token, "", sep == ' '
	case rest[0] != sep:
		return "", "", false
	}
	return token, rest[1:], true
}
