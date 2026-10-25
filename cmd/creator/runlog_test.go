package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"jagex_account_creator/internal/api"
	"jagex_account_creator/internal/config"
	"jagex_account_creator/internal/logtest"
)

// textTimeLayout is the time form of the slog text handler.
const textTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// textLine returns the one line that slog.NewTextHandler writes for a record
// at t with level, msg and attrs, with no newline.
func textLine(tb testing.TB, at time.Time, level slog.Level, msg string, attrs ...slog.Attr) string {
	tb.Helper()
	var buf bytes.Buffer
	r := slog.NewRecord(at, level, msg, 0)
	r.AddAttrs(attrs...)
	if err := newTextHandler(&buf, slog.LevelDebug).Handle(context.Background(), r); err != nil {
		tb.Fatal(err)
	}
	line, ok := strings.CutSuffix(buf.String(), "\n")
	if !ok || strings.Contains(line, "\n") {
		tb.Fatalf("the text handler wrote %q, want one line", buf.String())
	}
	return line
}

// recordLines are lines of the text handler with each attr kind that the tree logs.
func recordLines(tb testing.TB) []string {
	tb.Helper()
	at := time.Date(2026, 10, 21, 10, 4, 5, 123456789, time.FixedZone("", 2*60*60))
	// The keys that the text handler quotes are variables, so sloglint reads no literal key that breaks its case rule.
	spacedKey, quotedKey, backslashKey, emptyKey := "key with space", `key="quoted"`, `back\slash`, ""
	return []string{
		textLine(tb, at, slog.LevelError, "account creation failed",
			slog.Int("worker", 3), slog.Int64("account", 17),
			slog.String("outcome", "failure"),
			slog.Any("error", errors.New("waiting for clearance: cloudflare \"challenge\" timed out\nsecond line\x01")),
			slog.String("email", "user@example.com"),
			slog.String("empty", ""),
			slog.String("eq", "a=b"),
			slog.String("unicode", "héllo ✓"),
			slog.String("bad_utf8", "\xff\xfe"),
			slog.String(backslashKey, `C:\Users\x`),
			slog.Duration("delay", 1500*time.Millisecond),
			slog.Bool("2fa", true),
			slog.Int64("browser_bytes_sent", 149392),
			slog.Float64("ratio", 0.25),
			slog.Time("until", at.Add(time.Minute)),
			slog.String(spacedKey, "v"),
			slog.String(quotedKey, "v"),
			slog.String(emptyKey, "empty key"),
			slog.Group("steps", slog.Duration("browser_slot_wait", 0), slog.Duration("challenge", 4161*time.Millisecond)),
		),
		textLine(tb, at, slog.LevelDebug, "", slog.String("component", "browser")),
		textLine(tb, time.Time{}, slog.LevelWarn+2, "no time"),
	}
}

// reemit writes the parsed line l again through the text handler, with each
// value as a string, so the result equals the source line when the parse is exact.
func reemit(t *testing.T, l api.LogLine) string {
	t.Helper()
	var at time.Time
	if l.Time != "" {
		var err error
		if at, err = time.Parse(textTimeLayout, l.Time); err != nil {
			t.Fatalf("time %q: %v", l.Time, err)
		}
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(l.Level)); err != nil {
		t.Fatalf("level %q: %v", l.Level, err)
	}
	attrs := make([]slog.Attr, len(l.Attrs))
	for i, a := range l.Attrs {
		attrs[i] = slog.String(a.Key, a.Value)
	}
	return textLine(t, at, level, l.Msg, attrs...)
}

func TestParseLogLineRoundTrip(t *testing.T) {
	for _, line := range recordLines(t) {
		got := parseLogLine(7, line)
		if got.RunId != 7 || got.Attrs == nil {
			t.Errorf("parseLogLine(%q) = %+v, want run 7 and an attrs slice", line, got)
		}
		if again := reemit(t, got); again != line {
			t.Errorf("round trip of\n%s\ngave\n%s", line, again)
		}
	}

	first := parseLogLine(1, recordLines(t)[0])
	want := map[string]string{
		"error":                   "waiting for clearance: cloudflare \"challenge\" timed out\nsecond line\x01",
		"bad_utf8":                "\xff\xfe",
		"delay":                   "1.5s",
		"until":                   "2026-10-21T10:05:05.123+02:00",
		"key with space":          "v",
		`key="quoted"`:            "v",
		`back\slash`:              `C:\Users\x`,
		"":                        "empty key",
		"steps.browser_slot_wait": "0s",
		"steps.challenge":         "4.161s",
	}
	for _, a := range first.Attrs {
		if w, ok := want[a.Key]; ok && a.Value != w {
			t.Errorf("attr %q = %q, want %q", a.Key, a.Value, w)
		}
		delete(want, a.Key)
	}
	if len(want) != 0 || first.Time != "2026-10-21T10:04:05.123+02:00" || first.Level != "ERROR" || first.Msg != "account creation failed" {
		t.Errorf("line = %+v, missing %v, want the time, ERROR and the msg", first, want)
	}
	if noTime := parseLogLine(1, recordLines(t)[2]); noTime.Time != "" || noTime.Level != "WARN+2" || noTime.Msg != "no time" || len(noTime.Attrs) != 0 {
		t.Errorf("line with no time = %+v, want no time, WARN+2, the msg and no attrs", noTime)
	}
}

// A line that does not split keeps its whole text as msg, with no attrs.
func TestParseLogLineDoesNotSplit(t *testing.T) {
	for _, line := range []string{
		`level=INFO msg="open`,         // a quoted value with no end
		`level=INFO msg="a"b`,          // a quoted value followed by text
		`level=INFO "key"x=1`,          // a quoted key with no '='
		`level=INFO msg=a novalue`,     // a key with no '='
		`level=INFO msg=a "open=1`,     // a quoted key with no end
		"partial line\x00 with no key", // the text of a failed append
	} {
		got := parseLogLine(1, line)
		if got.Msg != line || got.Time != "" || got.Level != "" || got.Attrs == nil || len(got.Attrs) != 0 {
			t.Errorf("parseLogLine(%q) = %+v, want the whole text as msg", line, got)
		}
	}
}

func FuzzParseLogLine(f *testing.F) {
	for _, seed := range []struct{ key, value string }{
		{"error", "a \"quoted\" text\nwith a newline"},
		{"bad_utf8", "\xff\xfe"},
		{"key with space", "a=b"},
		{`"`, `\`},
		{"", "x"},
		{"steps.challenge", "4.161s"},
	} {
		f.Add(seed.key, seed.value)
	}
	// The seed lines also go in whole, as a value and as a line to parse.
	for _, line := range append(recordLines(f), `msg="open`, "a=b c", `"k"=v`, "=", `""=""`) {
		f.Add("line", line)
	}
	at := time.Date(2026, 10, 21, 10, 4, 5, 123e6, time.UTC)
	f.Fuzz(func(t *testing.T, key, value string) {
		// Any text parses without a panic.
		parseLogLine(1, value)
		if key == "" && value == "" {
			// slog drops an attr with no key and no value.
			return
		}

		line := textLine(t, at, slog.LevelInfo, "fuzz", slog.String(key, value))
		got := parseLogLine(1, line)
		want := []api.LogAttr{{Key: key, Value: value}}
		if got.Msg != "fuzz" || got.Level != "INFO" || len(got.Attrs) != 1 || got.Attrs[0] != want[0] {
			t.Errorf("parseLogLine(%q) = %+v, want the msg fuzz and the attr %+v", line, got, want)
		}
	})
}

func TestRunLog(t *testing.T) {
	t.Run("each line goes to the file, the base and the hub, with its end offset", func(t *testing.T) {
		dir := t.TempDir()
		database := filepath.Join(dir, "creator.db")
		logs := newHub[logLine](logLineBuffer)
		ch, cancel := logs.subscribe()
		defer cancel()
		var base bytes.Buffer
		var level slog.LevelVar

		l, logger, err := openRunLog(database, 4, runLogOptions{base: logtest.New(&base).Handler(), level: &level, logs: logs})
		if err != nil {
			t.Fatal(err)
		}
		logger.With("worker", 1).Info("first", "s", "a b")
		logger.Debug("hidden")
		level.Set(slog.LevelDebug)
		logger.Debug("second")
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}

		path := filepath.Join(dir, "logs", "run-4.log")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file := string(data)
		lines := strings.SplitAfter(file, "\n")
		if len(lines) != 3 || lines[2] != "" || !strings.Contains(lines[0], ` level=INFO msg=first worker=1 s="a b"`) || !strings.Contains(lines[1], " level=DEBUG msg=second") {
			t.Fatalf("file =\n%s\nwant the INFO line and the DEBUG line after the level change", file)
		}
		got, _ := published(ch)
		want := []logLine{
			{LogLine: parseLogLine(4, strings.TrimSuffix(lines[0], "\n")), end: int64(len(lines[0]))},
			{LogLine: parseLogLine(4, strings.TrimSuffix(lines[1], "\n")), end: int64(len(file))},
		}
		if !reflect.DeepEqual(got, want) || want[0].Msg != "first" {
			t.Errorf("published %+v, want %+v", got, want)
		}
		// The base handler keeps its own level, and logtest logs at DEBUG.
		if want := "level=INFO msg=first worker=1 s=\"a b\"\nlevel=DEBUG msg=hidden\nlevel=DEBUG msg=second\n"; base.String() != want {
			t.Errorf("base = %q, want %q", base.String(), want)
		}
		assertPrivate(t, path, 0o600)
		assertPrivate(t, filepath.Dir(path), 0o700|fs.ModeDir)
	})

	t.Run("the CLI has no hub, and the file takes the lines", func(t *testing.T) {
		database := filepath.Join(t.TempDir(), "creator.db")
		l, logger, err := openRunLog(database, 1, testLogOptions())
		if err != nil {
			t.Fatal(err)
		}
		logger.Info("cli")
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		if file := readRunLog(t, &config.Config{Database: database}, 1); !strings.HasSuffix(file, " level=INFO msg=cli\n") {
			t.Errorf("file = %q, want the cli line", file)
		}
	})

	t.Run("an existing file refuses the run and names the cause", func(t *testing.T) {
		database := filepath.Join(t.TempDir(), "creator.db")
		l, _, err := openRunLog(database, 1, testLogOptions())
		if err != nil {
			t.Fatal(err)
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}

		_, _, err = openRunLog(database, 1, testLogOptions())

		path := runLogPath(database, 1)
		if !errors.Is(err, fs.ErrExist) || !strings.HasPrefix(err.Error(), "creating the run log: ") || !strings.Contains(err.Error(), path) ||
			!strings.HasSuffix(err.Error(), " (a logs dir kept from a deleted database holds this file)") {
			t.Errorf("openRunLog() = %v, want fs.ErrExist that names %s and the cause", err, path)
		}
	})

	t.Run("a write or a close after the close fails", func(t *testing.T) {
		database := filepath.Join(t.TempDir(), "creator.db")
		logs := newHub[logLine](logLineBuffer)
		ch, cancel := logs.subscribe()
		defer cancel()
		l, _, err := openRunLog(database, 1, runLogOptions{base: logtest.New(io.Discard).Handler(), level: slog.LevelInfo, logs: logs})
		if err != nil {
			t.Fatal(err)
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}

		n, werr := l.Write([]byte("late\n"))
		cerr := l.Close()

		if n != 0 || werr == nil || !strings.HasPrefix(werr.Error(), "appending to the run log: ") {
			t.Errorf("Write() = %d, %v, want 0 and the append error", n, werr)
		}
		if !errors.Is(cerr, os.ErrClosed) || !strings.HasPrefix(cerr.Error(), "closing the run log: ") {
			t.Errorf("Close() = %v, want os.ErrClosed with the close name", cerr)
		}
		if got, _ := published(ch); len(got) != 0 {
			t.Errorf("published %+v, want no line of a failed append", got)
		}
	})
}

// assertPrivate fails the test when path does not have the mode want. Windows has no mode bits, so it checks nothing there.
func assertPrivate(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode() & (fs.ModePerm | fs.ModeDir); got != want {
		t.Errorf("mode of %s = %v, want %v", path, got, want)
	}
}

func TestReplayStart(t *testing.T) {
	tests := []struct {
		name   string
		lastID *string
		want   int64
	}{
		{"no id", nil, 0},
		{"an id of the run", new("4:120"), 120},
		{"an id at the end", new("4:500"), 500},
		{"an id at the start", new("4:0"), 0},
		{"an id of another run", new("3:120"), 0},
		{"an id past the end", new("4:501"), 0},
		{"a negative offset", new("4:-1"), 0},
		{"no colon", new("4"), 0},
		{"a run that does not parse", new("x:120"), 0},
		{"an offset that does not parse", new("4:x"), 0},
		{"an empty id", new(""), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := replayStart(tt.lastID, 4, 500); got != tt.want {
				t.Errorf("replayStart() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestReplayLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run-2.log")
	const file = "a=1\nb=22\nc=333\npartial"
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	collect := func(path string, from, end int64) ([]logLine, error) {
		var got []logLine
		for l, err := range replayLog(path, 2, from, end) {
			if err != nil {
				return got, err
			}
			got = append(got, l)
		}
		return got, nil
	}

	t.Run("the lines from an offset up to the end", func(t *testing.T) {
		got, err := collect(path, 4, 15)
		want := []logLine{{LogLine: parseLogLine(2, "b=22"), end: 9}, {LogLine: parseLogLine(2, "c=333"), end: 15}}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("replay = %+v, %v, want %+v", got, err, want)
		}
	})

	t.Run("an empty range has no line", func(t *testing.T) {
		if got, err := collect(path, 15, 15); err != nil || len(got) != 0 {
			t.Errorf("replay = %+v, %v, want no line", got, err)
		}
	})

	t.Run("a stop of the caller ends the replay", func(t *testing.T) {
		for l, err := range replayLog(path, 2, 0, 15) {
			if err != nil || !reflect.DeepEqual(l, logLine{LogLine: parseLogLine(2, "a=1"), end: 4}) {
				t.Errorf("first line = %+v, %v, want a=1", l, err)
			}
			break
		}
	})

	t.Run("a missing file is an error", func(t *testing.T) {
		_, err := collect(filepath.Join(dir, "run-3.log"), 0, 10)
		if !errors.Is(err, fs.ErrNotExist) || !strings.HasPrefix(err.Error(), "replaying the run log: ") {
			t.Errorf("replay error = %v, want fs.ErrNotExist with the replay name", err)
		}
	})

	t.Run("a read that fails is an error", func(t *testing.T) {
		// A dir opens on each OS, and a read of it fails.
		_, err := collect(dir, 0, 10)
		if err == nil || errors.Is(err, io.EOF) || !strings.HasPrefix(err.Error(), "replaying the run log: ") {
			t.Errorf("replay error = %v, want a read error with the replay name", err)
		}
	})
}
