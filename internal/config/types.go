package config

import "time"

// Config is the top-level application configuration.
type Config struct {
	Debug       bool              `yaml:"debug"`
	Database    string            `yaml:"database"` // path of the SQLite account store
	Chrome      ChromeConfig      `yaml:"chrome"`
	Proxy       ProxyConfig       `yaml:"proxy"`
	Email       EmailConfig       `yaml:"email"`
	Account     AccountConfig     `yaml:"account"`
	Concurrency ConcurrencyConfig `yaml:"concurrency"`
}

// ChromeConfig holds settings for the Chrome browser instance.
type ChromeConfig struct {
	Headless *bool         `yaml:"headless"` // nil defaults to true
	Timeout  time.Duration `yaml:"timeout"`
}

// ProxyConfig holds settings for proxy management. The proxy list is in the account store.
type ProxyConfig struct {
	Direct bool `yaml:"direct"` // run with no proxy and ignore the list
}

// The values of EmailConfig.Auth.
const (
	AuthPassword = "password" // LOGIN with IMAPPass
	AuthOAuth2   = "oauth2"   // XOAUTH2 with an access token from the refresh token of OAuth2
)

// EmailConfig holds settings for the email verification provider.
type EmailConfig struct {
	IMAPHost    string       `yaml:"imap_host"`
	IMAPPort    int          `yaml:"imap_port"`
	IMAPUser    string       `yaml:"imap_user"`
	IMAPPass    string       `yaml:"imap_pass"`    // used when Auth is AuthPassword
	IMAPDomains []string     `yaml:"imap_domains"` // each attempt takes one at random for its address
	Auth        string       `yaml:"auth"`         // AuthPassword or AuthOAuth2. Unset defaults to AuthPassword
	OAuth2      OAuth2Config `yaml:"oauth2"`       // used when Auth is AuthOAuth2
}

// OAuth2Config holds the refresh-token grant that gives the access tokens of an XOAUTH2 login.
type OAuth2Config struct {
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"` // empty for a public client
	RefreshToken string `yaml:"refresh_token"`
	TokenURL     string `yaml:"token_url"` // the token endpoint, for example https://oauth2.googleapis.com/token
}

// AccountConfig holds settings for generated accounts.
type AccountConfig struct {
	PasswordLength int   `yaml:"password_length"`
	EnableTOTP     *bool `yaml:"enable_totp"` // nil defaults to true
}

// ConcurrencyConfig holds settings for parallel workers.
type ConcurrencyConfig struct {
	MaxWorkers     int           `yaml:"max_workers"`
	MaxBrowsers    int           `yaml:"max_browsers"`    // cap on live Chrome processes. Unset (0) means MaxWorkers
	AccountCount   int           `yaml:"account_count"`   // -1 = unlimited (run until cancelled)
	AccountTimeout time.Duration `yaml:"account_timeout"` // bound per attempt up to the password POST. The work after it adds up to 60 s
}
