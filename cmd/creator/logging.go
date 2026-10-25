package main

import (
	"io"
	"log/slog"
	"strings"

	"github.com/lmittmann/tint"

	"jagex_account_creator/internal/bytesize"
)

// newLogger returns the logger of the run. A terminal gets the short view of
// terminalAttr. A file or a pipe gets every attribute as slog text.
func newLogger(w io.Writer, terminal, noColor bool, level slog.Leveler) *slog.Logger {
	if !terminal {
		return slog.New(newTextHandler(w, level))
	}
	return slog.New(tint.NewTextHandler(w, &tint.Options{
		Level:       level,
		TimeFormat:  "15:04:05.000",
		NoColor:     noColor,
		ReplaceAttr: terminalAttr,
	}))
}

// newTextHandler returns the handler of a file or a pipe: every attribute as
// slog text. The log file of a run uses it too, so it holds the same form.
func newTextHandler(w io.Writer, level slog.Leveler) *slog.TextHandler {
	return slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
}

// terminalAttr drops the step durations, shows the byte counts in binary units
// and makes the error red. A file keeps all of them for later analysis.
func terminalAttr(groups []string, a slog.Attr) slog.Attr {
	switch {
	case len(groups) > 0 && groups[0] == "steps":
		return slog.Attr{}
	case len(groups) > 0:
		return a
	case a.Key == "error":
		return tint.Attr(9, a)
	case a.Value.Kind() == slog.KindInt64 && strings.Contains(a.Key, "_bytes_"):
		return slog.String(a.Key, bytesize.Format(a.Value.Int64()))
	}
	return a
}
