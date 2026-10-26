package account

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"time"
)

const (
	lowerChars   = "abcdefghijklmnopqrstuvwxyz"
	upperChars   = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	digitChars   = "0123456789"
	specialChars = "!@#$%^&*"

	minAge = 18
	maxAge = 30
)

// GenerateName returns a 12-character random lowercase alphanumeric string. A
// new account takes one as its username and one as its email local part.
func GenerateName() string {
	const chars = lowerChars + digitChars
	const length = 12
	buf := make([]byte, length)
	for i := range buf {
		buf[i] = chars[cryptoRandIntn(len(chars))]
	}
	return string(buf)
}

// GenerateEmail returns a new account's email address: a GenerateName local
// part at one domain of domains, picked at random. The list must hold at least
// one domain. An empty list panics. The config validation is the only producer.
func GenerateEmail(domains []string) string {
	return GenerateName() + "@" + domains[cryptoRandIntn(len(domains))]
}

// GeneratePassword returns a cryptographically random password of the given
// length. The password is guaranteed to contain at least one uppercase letter,
// one lowercase letter, one digit, and one special character.
// The length must be at least 4, one byte for each required class. A shorter
// length panics. The config validation (minimum 8) is the only producer.
func GeneratePassword(length int) string {
	allChars := lowerChars + upperChars + digitChars + specialChars
	buf := make([]byte, length)

	// Guarantee at least one of each required class in positions 0-3.
	required := []string{lowerChars, upperChars, digitChars, specialChars}
	for i, pool := range required {
		buf[i] = pool[cryptoRandIntn(len(pool))]
	}

	// Fill the rest with random chars from the full set.
	for i := len(required); i < length; i++ {
		buf[i] = allChars[cryptoRandIntn(len(allChars))]
	}

	// Shuffle the buffer to avoid predictable positions for required classes.
	for i := length - 1; i > 0; i-- { // mutation-ignore: CONDITIONALS_BOUNDARY: equivalent: the added step swaps buf[0] with itself
		j := cryptoRandIntn(i + 1)
		buf[i], buf[j] = buf[j], buf[i]
	}

	return string(buf)
}

// GenerateDOB returns a random date of birth formatted as "YYYY-MM-DDT00:00:00.000Z"
// that makes the account holder between 18 and 30 years old.
func GenerateDOB() string {
	return generateDOBFrom(time.Now())
}

// generateDOBFrom picks a uniformly random day from the inclusive DOB range
// whose holder's age on the given date is in [minAge, maxAge].
func generateDOBFrom(now time.Time) string {
	earliest, latest := dobRange(now)
	dob := earliest.AddDate(0, 0, cryptoRandIntn(dobDays(earliest, latest)))
	return dob.Format("2006-01-02") + "T00:00:00.000Z"
}

// dobRange returns the inclusive range of birth dates whose holder is between
// minAge and maxAge on now. The bounds are UTC midnight, so a day count over
// them is exact under any input zone.
func dobRange(now time.Time) (earliest, latest time.Time) {
	now = now.UTC()
	now = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	latest = newestDOBWithAge(now, minAge)
	earliest = newestDOBWithAge(now, maxAge+1).AddDate(0, 0, 1)
	return earliest, latest
}

// dobDays counts the days in the inclusive range [earliest, latest].
func dobDays(earliest, latest time.Time) int {
	return int(latest.Sub(earliest)/(24*time.Hour)) + 1
}

// newestDOBWithAge returns the newest birth date whose holder is exactly age on now.
// AddDate moves a missing Feb 29 to Mar 1, and one day back from there the age holds.
func newestDOBWithAge(now time.Time, age int) time.Time {
	d := now.AddDate(-age, 0, 0)
	if d.AddDate(age, 0, 0).After(now) {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

// cryptoRandIntn returns a uniform random int in [0, n). n must be positive.
func cryptoRandIntn(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic(fmt.Errorf("reading a random int: %w", err)) //nolint:forbidigo // Unreachable: rand.Reader never returns an error.
	}
	return int(v.Int64())
}
