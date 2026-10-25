package account

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"
	_ "time/tzdata"
)

func TestGenerateName(t *testing.T) {
	for range 100 {
		u := GenerateName()
		if len(u) != 12 {
			t.Errorf("username length %d, want 12: %q", len(u), u)
		}
		for _, ch := range u {
			if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') {
				t.Errorf("username contains invalid char %q: %q", string(ch), u)
			}
		}
	}

	// Check uniqueness over a small sample.
	seen := make(map[string]struct{})
	for range 50 {
		u := GenerateName()
		seen[u] = struct{}{}
	}
	if len(seen) < 40 {
		t.Errorf("expected mostly unique usernames, got %d unique out of 50", len(seen))
	}
}

func TestGeneratePassword(t *testing.T) {
	tests := []struct {
		name   string
		length int
		want   int // expected output length
	}{
		{"default16", 16, 16},
		{"exact 8", 8, 8},
		{"long 32", 32, 32},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for range 50 {
				p := GeneratePassword(tt.length)
				if len(p) != tt.want {
					t.Errorf("password length = %d, want %d", len(p), tt.want)
				}

				var hasLower, hasUpper, hasDigit, hasSpecial bool
				for _, ch := range p {
					switch {
					case ch >= 'a' && ch <= 'z':
						hasLower = true
					case ch >= 'A' && ch <= 'Z':
						hasUpper = true
					case ch >= '0' && ch <= '9':
						hasDigit = true
					case strings.ContainsRune(specialChars, ch):
						hasSpecial = true
					default:
						t.Errorf("password contains unexpected char %q: %q", string(ch), p)
					}
				}
				if !hasLower {
					t.Errorf("password missing lowercase: %q", p)
				}
				if !hasUpper {
					t.Errorf("password missing uppercase: %q", p)
				}
				if !hasDigit {
					t.Errorf("password missing digit: %q", p)
				}
				if !hasSpecial {
					t.Errorf("password missing special char: %q", p)
				}
			}
		})
	}
}

func TestGeneratePasswordShuffles(t *testing.T) {
	// Before the shuffle, byte 0 is always a lowercase letter. With the shuffle,
	// 100 passwords in a row that start with one do not occur.
	for range 100 {
		p := GeneratePassword(16)
		if !strings.ContainsRune(lowerChars, rune(p[0])) {
			return
		}
	}
	t.Fatal("100 passwords all start with a lowercase letter, want shuffled positions")
}

// ageOn computes the full-year age of a person born on dob as of the UTC date of
// ref. The DOB names a UTC date.
func ageOn(dob, ref time.Time) int {
	ref = ref.UTC()
	age := ref.Year() - dob.Year()
	// If birthday hasn't occurred yet in the ref year, subtract 1.
	birthday := dob.AddDate(age, 0, 0)
	if birthday.After(ref) {
		age--
	}
	return age
}

func TestGenerateDOB(t *testing.T) {
	// Use a fixed reference time to avoid date-boundary edge cases.
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	for range 500 {
		dob := generateDOBFrom(now)

		if !strings.HasSuffix(dob, "T00:00:00.000Z") {
			t.Errorf("DOB missing time suffix: %q", dob)
		}

		parsed, err := time.Parse("2006-01-02T15:04:05.000Z", dob)
		if err != nil {
			t.Fatalf("failed to parse DOB %q: %v", dob, err)
		}

		age := ageOn(parsed, now)
		if age < 18 || age > 30 {
			t.Errorf("DOB %q yields age %d, want [18,30]", dob, age)
		}
	}
}

func TestGenerateDOBUsesTheClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for range 100 {
			dob := GenerateDOB()

			parsed, err := time.Parse("2006-01-02T15:04:05.000Z", dob)
			if err != nil {
				t.Fatalf("failed to parse DOB %q: %v", dob, err)
			}
			if age := ageOn(parsed, time.Now()); age < 18 || age > 30 {
				t.Fatalf("DOB %q yields age %d on %v, want [18,30]", dob, age, time.Now())
			}
		}
	})
}

// dobEdgeDates returns reference dates whose month and day boundaries stress the age bounds.
func dobEdgeDates() []time.Time {
	return []time.Time{
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),   // New Year's Day
		time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),  // mid-January
		time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), // end of year
		time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC),  // end of Feb (non-leap)
		time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC),  // leap day
		time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),  // mid-year
	}
}

func TestGenerateDOBEdgeCases(t *testing.T) {
	for _, now := range dobEdgeDates() {
		t.Run(now.Format("2006-01-02"), func(t *testing.T) {
			for range 500 {
				dob := generateDOBFrom(now)

				parsed, err := time.Parse("2006-01-02T15:04:05.000Z", dob)
				if err != nil {
					t.Fatalf("failed to parse DOB %q: %v", dob, err)
				}

				age := ageOn(parsed, now)
				if age < 18 || age > 30 {
					t.Errorf("now=%v DOB=%q age=%d, want [18,30]", now.Format("2006-01-02"), dob, age)
				}
			}
		})
	}
}

func TestDOBRangeBounds(t *testing.T) {
	for _, now := range dobEdgeDates() {
		t.Run(now.Format("2006-01-02"), func(t *testing.T) {
			earliest, latest := dobRange(now)

			if got := ageOn(latest, now); got != minAge {
				t.Errorf("ageOn(latest=%s) = %d, want %d", latest.Format("2006-01-02"), got, minAge)
			}
			// The day after latest must already be too young.
			if got := ageOn(latest.AddDate(0, 0, 1), now); got != minAge-1 {
				t.Errorf("ageOn(latest+1d) = %d, want %d", got, minAge-1)
			}
			if got := ageOn(earliest, now); got != maxAge {
				t.Errorf("ageOn(earliest=%s) = %d, want %d", earliest.Format("2006-01-02"), got, maxAge)
			}
			// The day before earliest must already be too old.
			if got := ageOn(earliest.AddDate(0, 0, -1), now); got != maxAge+1 {
				t.Errorf("ageOn(earliest-1d) = %d, want %d", got, maxAge+1)
			}
		})
	}
}

func TestDOBRangeDayCount(t *testing.T) {
	for _, now := range dobEdgeDates() {
		t.Run(now.Format("2006-01-02"), func(t *testing.T) {
			earliest, latest := dobRange(now)
			days := dobDays(earliest, latest)
			if last := earliest.AddDate(0, 0, days-1); !last.Equal(latest) {
				t.Errorf("earliest+%dd = %v, want latest %v", days-1, last, latest)
			}
		})
	}
}

func TestDOBRangeDSTZone(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}

	now := time.Date(2026, 3, 9, 12, 0, 0, 0, loc)
	earliest, latest := dobRange(now)

	days := dobDays(earliest, latest)
	if last := earliest.AddDate(0, 0, days-1); !last.Equal(latest) {
		t.Errorf("earliest+%dd = %v, want latest %v", days-1, last, latest)
	}

	for name, bound := range map[string]time.Time{"earliest": earliest, "latest": latest} {
		if bound.Location() != time.UTC {
			t.Errorf("%s location = %v, want UTC", name, bound.Location())
		}
		if h, m, s := bound.Clock(); h != 0 || m != 0 || s != 0 || bound.Nanosecond() != 0 {
			t.Errorf("%s = %v, want midnight", name, bound)
		}
	}
}

func TestDOBRangeZonedInput(t *testing.T) {
	// Each local date differs from the UTC date. The generated DOB claims UTC.
	tests := []struct {
		zone       string
		y, m, d, h int
		wantLatest string
	}{
		{"Australia/Sydney", 2026, 6, 15, 1, "2008-06-14"},      // the previous UTC day
		{"America/Los_Angeles", 1999, 12, 31, 16, "1982-01-01"}, // the next UTC day and year
	}
	for _, tt := range tests {
		t.Run(tt.zone, func(t *testing.T) {
			loc, err := time.LoadLocation(tt.zone)
			if err != nil {
				t.Fatalf("LoadLocation: %v", err)
			}
			now := time.Date(tt.y, time.Month(tt.m), tt.d, tt.h, 0, 0, 0, loc)
			earliest, latest := dobRange(now)

			if got := latest.Format("2006-01-02"); got != tt.wantLatest {
				t.Errorf("latest = %s, want %s (the UTC date minus minAge years)", got, tt.wantLatest)
			}
			if got := ageOn(latest, now); got != minAge {
				t.Errorf("ageOn(latest) = %d, want %d", got, minAge)
			}
			if got := ageOn(earliest.AddDate(0, 0, -1), now); got != maxAge+1 {
				t.Errorf("ageOn(earliest-1d) = %d, want %d", got, maxAge+1)
			}
		})
	}
}

func TestCryptoRandIntn(t *testing.T) {
	t.Run("range and distinctness", func(t *testing.T) {
		for _, n := range []int{1, 2, 10, 36, 70, 256, 5000} {
			seen := make(map[int]bool)
			for range 1000 {
				v := cryptoRandIntn(n)
				if v < 0 || v >= n {
					t.Fatalf("cryptoRandIntn(%d) = %d, out of range [0, %d)", n, v, n)
				}
				seen[v] = true
			}
			// For n > 1, we should see at least 2 distinct values.
			if n > 1 && len(seen) < 2 {
				t.Errorf("cryptoRandIntn(%d): only %d distinct values in 1000 calls", n, len(seen))
			}
		}
	})

	t.Run("n=1 always returns 0", func(t *testing.T) {
		for range 100 {
			v := cryptoRandIntn(1)
			if v != 0 {
				t.Fatalf("cryptoRandIntn(1) = %d, want 0", v)
			}
		}
	})
}

func TestGenerateDOBBoundaryAges(t *testing.T) {
	// Over many iterations, we should see both age 18 and age 30 produced.
	now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	seen := make(map[int]bool)
	for range 5000 {
		dob := generateDOBFrom(now)
		parsed, _ := time.Parse("2006-01-02T15:04:05.000Z", dob)
		seen[ageOn(parsed, now)] = true
	}

	if !seen[18] {
		t.Error("never generated age 18")
	}
	if !seen[30] {
		t.Error("never generated age 30")
	}
	// Ensure no out-of-range ages appeared.
	for age := range seen {
		if age < 18 || age > 30 {
			t.Errorf("generated out-of-range age %d", age)
		}
	}
}
