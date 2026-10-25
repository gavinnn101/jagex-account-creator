package logtest

import (
	"log/slog"
	"strings"
	"testing"
)

// TestNewDropsOnlyTheRecordTime pins the stripping condition. The record holds a
// top-level attribute and a group with its own time key. A change of either
// comparison, or of the `&&`, changes the line.
func TestNewDropsOnlyTheRecordTime(t *testing.T) {
	var out strings.Builder
	New(&out).Debug("hello", "host", "h.tx", slog.Group("req", "time", "kept"))

	const want = "level=DEBUG msg=hello host=h.tx req.time=kept\n"
	if got := out.String(); got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
}
