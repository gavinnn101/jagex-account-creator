package bytesize

import (
	"math"
	"testing"
)

func TestFormat(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0B"},
		{1023, "1023B"},
		{1024, "1.0KiB"},
		{1536, "1.5KiB"},
		{1 << 18, "256.0KiB"},
		{543210, "530.5KiB"},
		// The KiB to MiB edge: 1048524 B is 1023.949 KiB, and 1048525 B is 1023.950 KiB.
		{1048524, "1023.9KiB"},
		{1048525, "1.0MiB"},
		{1<<20 - 1, "1.0MiB"},
		{1 << 20, "1.0MiB"},
		{1258291, "1.2MiB"},
		{1 << 30, "1.0GiB"},
		{math.MaxInt64, "8.0EiB"},
	}
	for _, tt := range tests {
		if got := Format(tt.n); got != tt.want {
			t.Errorf("Format(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}
