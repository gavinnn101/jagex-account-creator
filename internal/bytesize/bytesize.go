// Package bytesize formats a byte count for a log line.
package bytesize

import (
	"math"
	"math/bits"
	"strconv"
)

// Format returns n in binary units with one decimal and no space, for example
// 530.5KiB. A count below 1024 has no decimal, for example 512B. A value with
// no space needs no quotes in a log line.
func Format(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + "B"
	}
	// exp is the largest power of 1024 that is not more than n: 1 for KiB.
	exp := (bits.Len64(uint64(n)) - 1) / 10
	tenths := math.Round(math.Ldexp(float64(n), -10*exp) * 10)
	// A value that rounds to 1024.0 is 1.0 in the next unit.
	if tenths >= 10240 {
		exp++
		tenths = 10
	}
	return strconv.FormatFloat(tenths/10, 'f', 1, 64) + "KMGTPE"[exp-1:exp] + "iB"
}
