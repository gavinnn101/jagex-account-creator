// Package account holds the account record and generates the data of a new
// account and its TOTP codes.
package account

import "time"

// Account represents a Jagex account that Jagex created or may have created.
// The byte fields count the bytes to and from the upstream proxy in each
// phase. A direct connection has none.
type Account struct {
	Email                string    `json:"email"`
	Username             string    `json:"username"`
	Password             string    `json:"password"`
	TOTPSecret           string    `json:"totp_secret,omitempty"`
	BackupCodes          []string  `json:"backup_codes,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
	Proxy                string    `json:"proxy,omitempty"`
	BrowserBytesSent     int64     `json:"browser_bytes_sent,omitempty"`
	BrowserBytesReceived int64     `json:"browser_bytes_received,omitempty"`
	HTTPBytesSent        int64     `json:"http_bytes_sent,omitempty"`
	HTTPBytesReceived    int64     `json:"http_bytes_received,omitempty"`
	// Unconfirmed is true when the process did not see Jagex confirm the last
	// change to the account. With no TOTPSecret, that change is the creation.
	// With a TOTPSecret and no BackupCodes, it is the 2FA activation. The
	// operator checks such an account by hand, and a confirm then sets it to false.
	Unconfirmed bool `json:"unconfirmed,omitempty"`
}
