// Package logtest provides a text logger that captures log lines for a test.
package logtest

import (
	"io"
	"log/slog"
)

// New returns a logger that writes every level to w as text lines. It drops the
// time of the record, so that a test can compare whole lines.
func New(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// The group guard keeps a time key that an attribute group holds.
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
}
