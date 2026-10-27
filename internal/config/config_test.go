package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestApplyDefaults(t *testing.T) {
	cfg := &Config{}
	applyDefaults(cfg)

	if cfg.Chrome.Timeout != 60*time.Second {
		t.Errorf("Chrome.Timeout = %v, want 60s", cfg.Chrome.Timeout)
	}
	if cfg.Chrome.Headless == nil || !*cfg.Chrome.Headless {
		t.Errorf("Chrome.Headless = %v, want true", cfg.Chrome.Headless)
	}
	if cfg.Account.EnableTOTP == nil || !*cfg.Account.EnableTOTP {
		t.Errorf("Account.EnableTOTP = %v, want true", cfg.Account.EnableTOTP)
	}
	if cfg.Account.PasswordLength != 16 {
		t.Errorf("Account.PasswordLength = %d, want 16", cfg.Account.PasswordLength)
	}
	if cfg.Database != "creator.db" {
		t.Errorf("Database = %q, want %q", cfg.Database, "creator.db")
	}
	if cfg.Concurrency.MaxWorkers != 1 {
		t.Errorf("Concurrency.MaxWorkers = %d, want 1", cfg.Concurrency.MaxWorkers)
	}
	if cfg.Concurrency.AccountCount != 1 {
		t.Errorf("Concurrency.AccountCount = %d, want 1", cfg.Concurrency.AccountCount)
	}
	if cfg.Concurrency.AccountTimeout != 5*time.Minute {
		t.Errorf("Concurrency.AccountTimeout = %v, want 5m", cfg.Concurrency.AccountTimeout)
	}
	if cfg.Email.IMAPPort != 993 {
		t.Errorf("Email.IMAPPort = %d, want 993", cfg.Email.IMAPPort)
	}
	if cfg.Email.IMAPDomains == nil || len(cfg.Email.IMAPDomains) != 0 {
		t.Errorf("Email.IMAPDomains = %#v, want an empty non-nil list", cfg.Email.IMAPDomains)
	}
}

func TestApplyDefaultsPreservesExplicitValues(t *testing.T) {
	cfg := &Config{
		Database: "custom.db",
		Chrome: ChromeConfig{
			Headless: new(false),
			Timeout:  30 * time.Second,
		},
		Account: AccountConfig{
			PasswordLength: 20,
			EnableTOTP:     new(false),
		},
		Concurrency: ConcurrencyConfig{
			MaxWorkers:     5,
			AccountCount:   10,
			AccountTimeout: 3 * time.Minute,
		},
		Email: EmailConfig{
			IMAPPort:    143,
			IMAPDomains: []string{"example.com"},
		},
	}
	applyDefaults(cfg)

	if cfg.Chrome.Timeout != 30*time.Second {
		t.Errorf("Chrome.Timeout = %v, want 30s", cfg.Chrome.Timeout)
	}
	if cfg.Chrome.Headless == nil || *cfg.Chrome.Headless {
		t.Errorf("Chrome.Headless = %v, want false (explicit)", cfg.Chrome.Headless)
	}
	if cfg.Account.EnableTOTP == nil || *cfg.Account.EnableTOTP {
		t.Errorf("Account.EnableTOTP = %v, want false (explicit)", cfg.Account.EnableTOTP)
	}
	if cfg.Account.PasswordLength != 20 {
		t.Errorf("Account.PasswordLength = %d, want 20", cfg.Account.PasswordLength)
	}
	if cfg.Database != "custom.db" {
		t.Errorf("Database = %q, want %q", cfg.Database, "custom.db")
	}
	if cfg.Concurrency.MaxWorkers != 5 {
		t.Errorf("Concurrency.MaxWorkers = %d, want 5", cfg.Concurrency.MaxWorkers)
	}
	if cfg.Concurrency.AccountCount != 10 {
		t.Errorf("Concurrency.AccountCount = %d, want 10", cfg.Concurrency.AccountCount)
	}
	if cfg.Concurrency.AccountTimeout != 3*time.Minute {
		t.Errorf("Concurrency.AccountTimeout = %v, want 3m", cfg.Concurrency.AccountTimeout)
	}
	if cfg.Email.IMAPPort != 143 {
		t.Errorf("Email.IMAPPort = %d, want 143", cfg.Email.IMAPPort)
	}
	if !slices.Equal(cfg.Email.IMAPDomains, []string{"example.com"}) {
		t.Errorf("Email.IMAPDomains = %q, want [example.com]", cfg.Email.IMAPDomains)
	}
}

func TestApplyDefaultsUnlimitedAccountCount(t *testing.T) {
	// An AccountCount of -1 means unlimited. applyDefaults must not override it.
	cfg := &Config{
		Concurrency: ConcurrencyConfig{
			AccountCount: -1,
		},
	}
	applyDefaults(cfg)

	if cfg.Concurrency.AccountCount != -1 {
		t.Errorf("Concurrency.AccountCount = %d, want -1 (unlimited)", cfg.Concurrency.AccountCount)
	}
}

// validIMAPYAML is a minimal valid email section. Tests of non-email config
// append their YAML to it.
const validIMAPYAML = "email:\n  imap_host: mail.example.com\n  imap_user: user\n  imap_pass: pass\n  imap_domains:\n    - example.com\n"

// validEmail is the struct form of validIMAPYAML.
func validEmail() EmailConfig {
	return EmailConfig{IMAPHost: "mail.example.com", IMAPUser: "user", IMAPPass: "pass", IMAPDomains: []string{"example.com"}}
}

func TestLoadAccountCount(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		wantCount int
	}{
		{
			name:      "not set defaults to 1",
			yaml:      validIMAPYAML + "concurrency:\n  max_workers: 2\n",
			wantCount: 1,
		},
		{
			name:      "explicit positive value",
			yaml:      validIMAPYAML + "concurrency:\n  max_workers: 2\n  account_count: 5\n",
			wantCount: 5,
		},
		{
			name:      "unlimited mode",
			yaml:      validIMAPYAML + "concurrency:\n  max_workers: 2\n  account_count: -1\n",
			wantCount: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, tt.yaml))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Concurrency.AccountCount != tt.wantCount {
				t.Errorf("AccountCount = %d, want %d", cfg.Concurrency.AccountCount, tt.wantCount)
			}
		})
	}
}

func TestLoadAccountTimeout(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		wantTimeout time.Duration
	}{
		{
			name:        "not set defaults to 5m",
			yaml:        validIMAPYAML,
			wantTimeout: 5 * time.Minute,
		},
		{
			name:        "explicit value",
			yaml:        validIMAPYAML + "concurrency:\n  account_timeout: 3m\n",
			wantTimeout: 3 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, tt.yaml))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Concurrency.AccountTimeout != tt.wantTimeout {
				t.Errorf("AccountTimeout = %v, want %v", cfg.Concurrency.AccountTimeout, tt.wantTimeout)
			}
		})
	}
}

func TestLoadMaxBrowsers(t *testing.T) {
	tests := []struct {
		name         string
		yaml         string
		wantBrowsers int
	}{
		{
			name:         "not set defaults to max_workers",
			yaml:         validIMAPYAML + "concurrency:\n  max_workers: 3\n",
			wantBrowsers: 3,
		},
		{
			name:         "explicit value",
			yaml:         validIMAPYAML + "concurrency:\n  max_workers: 3\n  max_browsers: 2\n",
			wantBrowsers: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, tt.yaml))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Concurrency.MaxBrowsers != tt.wantBrowsers {
				t.Errorf("MaxBrowsers = %d, want %d", cfg.Concurrency.MaxBrowsers, tt.wantBrowsers)
			}
		})
	}
}

func TestLoadBools(t *testing.T) {
	enableTOTP := func(cfg *Config) *bool { return cfg.Account.EnableTOTP }
	headless := func(cfg *Config) *bool { return cfg.Chrome.Headless }
	tests := []struct {
		name  string
		yaml  string
		field func(*Config) *bool
		want  bool
	}{
		{name: "enable_totp not set defaults to true", yaml: validIMAPYAML, field: enableTOTP, want: true},
		{name: "enable_totp explicitly true", yaml: validIMAPYAML + "account:\n  enable_totp: true\n", field: enableTOTP, want: true},
		{name: "enable_totp explicitly false", yaml: validIMAPYAML + "account:\n  enable_totp: false\n", field: enableTOTP, want: false},
		{name: "headless not set defaults to true", yaml: validIMAPYAML, field: headless, want: true},
		{name: "headless explicitly true", yaml: validIMAPYAML + "chrome:\n  headless: true\n", field: headless, want: true},
		{name: "headless explicitly false", yaml: validIMAPYAML + "chrome:\n  headless: false\n", field: headless, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, tt.yaml))
			if err != nil {
				t.Fatal(err)
			}
			got := tt.field(cfg)
			if got == nil {
				t.Fatal("field should not be nil after Load")
			}
			if *got != tt.want {
				t.Errorf("field = %v, want %v", *got, tt.want)
			}
		})
	}
}

func TestLoadFileNotFound(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if !errors.Is(err, fs.ErrNotExist) || !strings.HasPrefix(err.Error(), "reading config file: ") {
		t.Fatalf("error = %v, want a reading config file error that matches fs.ErrNotExist", err)
	}
}

// writeConfig writes content to a config file in a temporary directory and
// returns its path.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadOverrides(t *testing.T) {
	const countErr = "concurrency.account_count must be positive or -1 (unlimited)"
	tests := []struct {
		name      string
		count     int
		wantCount int
		wantErr   string
	}{
		{name: "positive replaces the file value", count: 7, wantCount: 7},
		{name: "unlimited", count: -1, wantCount: -1},
		{name: "zero runs after the defaults", count: 0, wantErr: countErr + ", got 0"},
		{name: "negative two fails validation", count: -2, wantErr: countErr + ", got -2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, validIMAPYAML+"concurrency:\n  account_count: 5\n")
			cfg, err := Load(path, func(cfg *Config) { cfg.Concurrency.AccountCount = tt.count })
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Concurrency.AccountCount != tt.wantCount {
				t.Errorf("AccountCount = %d, want %d", cfg.Concurrency.AccountCount, tt.wantCount)
			}
		})
	}
}

func TestLoadOverridesRunInOrder(t *testing.T) {
	path := writeConfig(t, validIMAPYAML)
	cfg, err := Load(path,
		func(cfg *Config) { cfg.Concurrency.AccountCount = 3 },
		func(cfg *Config) { cfg.Concurrency.AccountCount = 4 },
	)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Concurrency.AccountCount != 4 {
		t.Errorf("AccountCount = %d, want 4 (the last override)", cfg.Concurrency.AccountCount)
	}
}

func TestLoadDecodeError(t *testing.T) {
	for _, tt := range []struct{ name, yaml, want string }{
		{"unknown key", validIMAPYAML + "  provider: imap\n", "provider"},
		{"removed output_file key", validIMAPYAML + "account:\n  output_file: accounts.jsonl\n", "output_file"},
		{"removed proxy.file key", validIMAPYAML + "proxy:\n  file: proxies.txt\n", "file"},
		{"malformed YAML", validIMAPYAML + ":::invalid\n", "yaml:"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.yaml))
			if err == nil || !strings.HasPrefix(err.Error(), "parsing config file: ") ||
				strings.Contains(err.Error(), "invalid config") || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want a parsing config file error that names %q", err, tt.want)
			}
		})
	}
}

func TestLoadEmptyFile(t *testing.T) {
	_, err := Load(writeConfig(t, ""))
	if err == nil {
		t.Fatal("expected the required-field errors for an empty file")
	}
	for _, want := range []string{"invalid config", "imap_host", "imap_user", "imap_pass"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
}

// The sample's IMAP fields are empty, so only the decode step of Load runs on it.
func TestShippedConfigDecodes(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decode(data); err != nil {
		t.Fatalf("config.yaml: %v", err)
	}
}

func TestValidateValidIMAPConfig(t *testing.T) {
	cfg := &Config{
		Email: validEmail(),
	}
	applyDefaults(cfg)
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}
}

func TestValidateFields(t *testing.T) {
	const (
		passwordErr = "account.password_length must be at least 8, got "
		workersErr  = "concurrency.max_workers must be at least 1, got "
		countErr    = "concurrency.account_count must be positive or -1 (unlimited), got "
		portErr     = "email.imap_port must be between 1 and 65535, got "
	)
	tests := []struct {
		name    string
		set     func(*Config)
		wantErr string // empty means valid
	}{
		{"password_length too short", func(c *Config) { c.Account.PasswordLength = 4 }, passwordErr + "4"},
		{"password_length negative", func(c *Config) { c.Account.PasswordLength = -1 }, passwordErr + "-1"},
		{"password_length minimum valid", func(c *Config) { c.Account.PasswordLength = 8 }, ""},
		{"password_length generous", func(c *Config) { c.Account.PasswordLength = 32 }, ""},
		{"chrome.timeout negative", func(c *Config) { c.Chrome.Timeout = -5 * time.Second }, "chrome.timeout must be positive, got -5s"},
		{"chrome.timeout zero", func(c *Config) { c.Chrome.Timeout = 0 }, "chrome.timeout must be positive, got 0s"},
		{"max_workers zero", func(c *Config) { c.Concurrency.MaxWorkers = 0 }, workersErr + "0"},
		{"max_workers negative", func(c *Config) { c.Concurrency.MaxWorkers = -1 }, workersErr + "-1"},
		{"max_workers one", func(c *Config) { c.Concurrency.MaxWorkers = 1 }, ""},
		{"max_workers many", func(c *Config) { c.Concurrency.MaxWorkers = 10 }, ""},
		{"account_count zero", func(c *Config) { c.Concurrency.AccountCount = 0 }, countErr + "0"},
		{"account_count negative two", func(c *Config) { c.Concurrency.AccountCount = -2 }, countErr + "-2"},
		{"account_count unlimited", func(c *Config) { c.Concurrency.AccountCount = -1 }, ""},
		{"account_count one", func(c *Config) { c.Concurrency.AccountCount = 1 }, ""},
		{"account_count many", func(c *Config) { c.Concurrency.AccountCount = 100 }, ""},
		{"imap_port zero", func(c *Config) { c.Email.IMAPPort = 0 }, portErr + "0"},
		{"imap_port negative", func(c *Config) { c.Email.IMAPPort = -1 }, portErr + "-1"},
		{"imap_port too high", func(c *Config) { c.Email.IMAPPort = 65536 }, portErr + "65536"},
		{"imap_port min valid", func(c *Config) { c.Email.IMAPPort = 1 }, ""},
		{"imap_port max valid", func(c *Config) { c.Email.IMAPPort = 65535 }, ""},
		{"imap_port standard", func(c *Config) { c.Email.IMAPPort = 993 }, ""},
		{"account_timeout negative", func(c *Config) { c.Concurrency.AccountTimeout = -1 * time.Second }, "concurrency.account_timeout must be positive, got -1s"},
		{"account_timeout zero", func(c *Config) { c.Concurrency.AccountTimeout = 0 }, "concurrency.account_timeout must be positive, got 0s"},
		{"database empty", func(c *Config) { c.Database = "" }, "database must not be empty"},
		{"imap_domains nil with a user that is no address", func(c *Config) { c.Email.IMAPDomains = nil },
			`email.imap_user must be an email address when email.imap_domains is empty, got "user"`},
		{"imap_domains empty with an address as the user", func(c *Config) { c.Email.IMAPDomains, c.Email.IMAPUser = []string{}, "user@example.com" }, ""},
		{"imap_domains bad entry", func(c *Config) { c.Email.IMAPDomains = []string{"localhost"} }, `email.imap_domains[0] "localhost" is not a valid domain name`},
		{"imap_domains two domains", func(c *Config) { c.Email.IMAPDomains = []string{"a.example.com", "b.example.com"} }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Email: validEmail()}
			applyDefaults(cfg)
			// The value is set after the defaults, so a zero value reaches Validate.
			tt.set(cfg)
			err := Validate(cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid config, got: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("Validate error = %v, want exactly %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateDomains(t *testing.T) {
	invalid := func(i int, d string) string {
		return fmt.Sprintf("email.imap_domains[%d] %q is not a valid domain name", i, d)
	}
	tests := []struct {
		name    string
		domains []string
		want    []string // the error texts in order. Empty means valid
	}{
		{name: "nil"},
		{name: "empty", domains: []string{}},
		{name: "one domain", domains: []string{"penguinlover.me"}},
		{name: "many labels", domains: []string{"sub.example.co.uk"}},
		{name: "the xn-- form", domains: []string{"xn--bcher-kva.example"}},
		{name: "upper case passes through the fold", domains: []string{"Sugmad.ME"}},
		{name: "a space", domains: []string{"foo bar"}, want: []string{invalid(0, "foo bar")}},
		{name: "an email address", domains: []string{"me@penguinlover.me"}, want: []string{invalid(0, "me@penguinlover.me")}},
		{name: "a URL", domains: []string{"https://sugmad.me"}, want: []string{invalid(0, "https://sugmad.me")}},
		{name: "an underscore", domains: []string{"under_score.me"}, want: []string{invalid(0, "under_score.me")}},
		{name: "a leading space", domains: []string{" sugmad.me"}, want: []string{invalid(0, " sugmad.me")}},
		{name: "no dot", domains: []string{"localhost"}, want: []string{invalid(0, "localhost")}},
		{name: "a trailing dot", domains: []string{"sugmad.me."}, want: []string{invalid(0, "sugmad.me.")}},
		{name: "an IP address", domains: []string{"1.2.3.4"}, want: []string{invalid(0, "1.2.3.4")}},
		{name: "a last label of digits after a letter", domains: []string{"example.123"}, want: []string{invalid(0, "example.123")}},
		{name: "a non-ASCII form", domains: []string{"bücher.example"}, want: []string{invalid(0, "bücher.example")}},
		{name: "a rune that folds to an ASCII letter", domains: []string{"\u212Aelvin.com"}, want: []string{invalid(0, "\u212Aelvin.com")}},
		{name: "a lone 0x80 byte", domains: []string{"\x80.example.com"}, want: []string{invalid(0, "\x80.example.com")}},
		{name: "a blank entry gets one error", domains: []string{"a.example.com", " \t"}, want: []string{"email.imap_domains[1] is empty"}},
		{name: "two blank entries", domains: []string{"", " "}, want: []string{"email.imap_domains[0] is empty", "email.imap_domains[1] is empty"}},
		{name: "a repeat in another case", domains: []string{"Sugmad.ME", "penguinlover.me", "sugmad.me"}, want: []string{`email.imap_domains[2] "sugmad.me" repeats [0]`}},
		{name: "a third copy repeats the first", domains: []string{"a.example.com", "a.example.com", "A.example.com"},
			want: []string{`email.imap_domains[1] "a.example.com" repeats [0]`, `email.imap_domains[2] "A.example.com" repeats [0]`}},
		{name: "a bad entry is no repeat of a bad entry", domains: []string{"localhost", "a.example.com", "LOCALHOST"},
			want: []string{invalid(0, "localhost"), invalid(2, "LOCALHOST")}},
		{name: "the entry errors come before the repeats", domains: []string{"a.example.com", "a.example.com", "localhost"},
			want: []string{invalid(2, "localhost"), `email.imap_domains[1] "a.example.com" repeats [0]`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, err := range validateDomains("email.imap_domains", tt.domains) {
				got = append(got, err.Error())
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("validateDomains(%q) = %q, want %q", tt.domains, got, tt.want)
			}
		})
	}
}

func TestValidateIMAPRequiredFields(t *testing.T) {
	tests := []struct {
		name string
		cfg  EmailConfig
		want string // substring expected in error
	}{
		{
			name: "missing host",
			cfg:  EmailConfig{IMAPUser: "u", IMAPPass: "p", IMAPDomains: []string{"d"}},
			want: "imap_host",
		},
		{
			name: "missing user",
			cfg:  EmailConfig{IMAPHost: "h", IMAPPass: "p", IMAPDomains: []string{"d"}},
			want: "imap_user",
		},
		{
			name: "missing pass",
			cfg:  EmailConfig{IMAPHost: "h", IMAPUser: "u", IMAPDomains: []string{"d"}},
			want: "imap_pass",
		},
		{
			name: "missing domain with a user that is no address",
			cfg:  EmailConfig{IMAPHost: "h", IMAPUser: "u", IMAPPass: "p"},
			want: `email.imap_user must be an email address when email.imap_domains is empty, got "u"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Email: tt.cfg}
			applyDefaults(cfg)
			cfg.Email = tt.cfg       // restore test values after defaults
			cfg.Email.IMAPPort = 993 // avoid port error
			cfg.Email.Auth = AuthPassword
			err := Validate(cfg)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("expected error to mention %q, got: %v", tt.want, err)
			}
		})
	}
}

func TestValidateNoDomain(t *testing.T) {
	const countErr = "concurrency.account_count must not exceed %d, the count of the mailboxes, " +
		"because a mailbox with no imap_domains makes one account, at its own address, got %d"
	mailbox := func(user string, domains ...string) MailboxConfig {
		return MailboxConfig{IMAPHost: "mail.example.org", IMAPPort: 993, IMAPUser: user, IMAPPass: "pass", IMAPDomains: domains, Auth: AuthPassword}
	}
	threeMailboxes := func(c *Config) {
		c.Email.Mailboxes = []MailboxConfig{mailbox("a@example.org"), mailbox("b@example.org"), mailbox("c@example.org")}
	}
	tests := []struct {
		name    string
		set     func(*Config)
		wantErr string // empty means valid
	}{
		{"one account as the user", func(*Config) {}, ""},
		{"two accounts from one mailbox", func(c *Config) { c.Concurrency.AccountCount = 2 }, fmt.Sprintf(countErr, 1, 2)},
		{"no account limit makes one account per mailbox", func(c *Config) { c.Concurrency.AccountCount = -1 }, ""},
		{"one account per mailbox of the list", func(c *Config) { threeMailboxes(c); c.Concurrency.AccountCount = 3 }, ""},
		{"more accounts than the mailboxes of the list", func(c *Config) { threeMailboxes(c); c.Concurrency.AccountCount = 4 }, fmt.Sprintf(countErr, 3, 4)},
		{"a mailbox of the list with domains lifts the limit", func(c *Config) {
			threeMailboxes(c)
			c.Email.Mailboxes[1].IMAPDomains, c.Concurrency.AccountCount = []string{"example.com"}, 50
		}, ""},
		{"a mailbox of the list with no host", func(c *Config) { threeMailboxes(c); c.Email.Mailboxes[1].IMAPHost = "" },
			"email.mailboxes[1].imap_host is required"},
		{"a mailbox of the list whose oauth2 has no client", func(c *Config) {
			threeMailboxes(c)
			c.Email.Mailboxes[2].Auth = AuthOAuth2
			c.Email.Mailboxes[2].OAuth2 = OAuth2Config{RefreshToken: "refresh", TokenURL: "https://login.example.org/token"}
		}, "email.mailboxes[2].oauth2.client_id is required"},
		{"two mailboxes of the list with one address", func(c *Config) { threeMailboxes(c); c.Email.Mailboxes[2].IMAPUser = "A@example.org" },
			`email.mailboxes[2].imap_user repeats the address of email.mailboxes[0], "A@example.org"`},
		{"two mailboxes of the list with one address and domains", func(c *Config) {
			threeMailboxes(c)
			c.Email.Mailboxes[2].IMAPUser, c.Email.Mailboxes[2].IMAPDomains = "a@example.org", []string{"example.com"}
		}, ""},
		{"a user that is no address", func(c *Config) { c.Email.IMAPUser = "user" },
			`email.imap_user must be an email address when email.imap_domains is empty, got "user"`},
		{"domains allow a user that is no address and many accounts", func(c *Config) {
			c.Email.IMAPDomains, c.Email.IMAPUser, c.Concurrency.AccountCount = []string{"example.com"}, "user", 5
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Email: validEmail()}
			cfg.Email.IMAPDomains, cfg.Email.IMAPUser = nil, "user@example.org"
			applyDefaults(cfg)
			tt.set(cfg)
			err := Validate(cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid config, got: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("Validate error = %v, want exactly %q", err, tt.wantErr)
			}
		})
	}
}

func TestMailboxDefaults(t *testing.T) {
	cfg := &Config{Email: EmailConfig{Mailboxes: []MailboxConfig{{}, {IMAPPort: 143, Auth: AuthOAuth2}}}}
	applyDefaults(cfg)
	if got := cfg.Email.Mailboxes[0]; got.IMAPPort != 993 || got.IMAPDomains == nil || len(got.IMAPDomains) != 0 || got.Auth != AuthPassword {
		t.Errorf("Mailboxes[0] = %+v, want port 993, an empty domain list and auth %q", got, AuthPassword)
	}
	if got := cfg.Email.Mailboxes[1]; got.IMAPPort != 143 || got.Auth != AuthOAuth2 {
		t.Errorf("Mailboxes[1] = %+v, want its own port 143 and auth %q", got, AuthOAuth2)
	}
}

// The email section of a mailbox holds each key of the mailbox, and Mailbox gives the mailbox back.
func TestMailboxEmailConfig(t *testing.T) {
	m := MailboxConfig{IMAPHost: "h", IMAPPort: 143, IMAPUser: "u@example.com", IMAPPass: "p", IMAPDomains: []string{"example.com"},
		Auth: AuthOAuth2, OAuth2: validOAuth2()}
	e := m.EmailConfig()
	if e.Mailboxes != nil || !reflect.DeepEqual(e.Mailbox(), m) {
		t.Errorf("EmailConfig() = %+v, want the keys of %+v and no mailboxes list", e, m)
	}
}

func TestDefaultAuth(t *testing.T) {
	cfg := &Config{}
	applyDefaults(cfg)
	if cfg.Email.Auth != AuthPassword {
		t.Errorf("Email.Auth = %q, want %q", cfg.Email.Auth, AuthPassword)
	}
	cfg = &Config{Email: EmailConfig{Auth: AuthOAuth2}}
	applyDefaults(cfg)
	if cfg.Email.Auth != AuthOAuth2 {
		t.Errorf("Email.Auth = %q, want %q (explicit)", cfg.Email.Auth, AuthOAuth2)
	}
}

// validOAuth2 returns a complete oauth2 section of a confidential client.
func validOAuth2() OAuth2Config {
	return OAuth2Config{ClientID: "client", ClientSecret: "secret", RefreshToken: "refresh", TokenURL: "https://oauth2.example.com/token"}
}

func TestValidateAuth(t *testing.T) {
	const urlErr = "email.oauth2.token_url must be an http or https URL, got "
	// oauth2 sets auth oauth2 with a complete section, and then edit.
	oauth2 := func(edit func(*OAuth2Config)) func(*EmailConfig) {
		return func(e *EmailConfig) {
			e.Auth, e.IMAPPass, e.OAuth2 = AuthOAuth2, "", validOAuth2()
			edit(&e.OAuth2)
		}
	}
	tests := []struct {
		name    string
		set     func(*EmailConfig)
		wantErr string // empty means valid
	}{
		{"password with a password", func(*EmailConfig) {}, ""},
		{"password with no password", func(e *EmailConfig) { e.IMAPPass = "" }, "email.imap_pass is required"},
		{"oauth2 complete", oauth2(func(*OAuth2Config) {}), ""},
		{"oauth2 of a public client", oauth2(func(o *OAuth2Config) { o.ClientSecret = "" }), ""},
		{"oauth2 over http", oauth2(func(o *OAuth2Config) { o.TokenURL = "http://127.0.0.1:8080/token" }), ""},
		{"oauth2 with no client_id", oauth2(func(o *OAuth2Config) { o.ClientID = "" }), "email.oauth2.client_id is required"},
		{"oauth2 with no refresh_token", oauth2(func(o *OAuth2Config) { o.RefreshToken = "" }), "email.oauth2.refresh_token is required"},
		{"oauth2 with no token_url", oauth2(func(o *OAuth2Config) { o.TokenURL = "" }), "email.oauth2.token_url is required"},
		{"oauth2 with a token_url of another scheme", oauth2(func(o *OAuth2Config) { o.TokenURL = "ftp://oauth2.example.com/token" }),
			urlErr + `"ftp://oauth2.example.com/token"`},
		{"oauth2 with a token_url of no host", oauth2(func(o *OAuth2Config) { o.TokenURL = "https:///token" }), urlErr + `"https:///token"`},
		{"oauth2 with a token_url that does not parse", oauth2(func(o *OAuth2Config) { o.TokenURL = "https://oauth2.example.com/%zz" }),
			urlErr + `"https://oauth2.example.com/%zz"`},
		{"oauth2 with an empty section", oauth2(func(o *OAuth2Config) { *o = OAuth2Config{} }),
			"email.oauth2.client_id is required\nemail.oauth2.refresh_token is required\nemail.oauth2.token_url is required"},
		{"an unknown auth", func(e *EmailConfig) { e.Auth = "plain" }, `email.auth must be "password" or "oauth2", got "plain"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Email: validEmail()}
			applyDefaults(cfg)
			tt.set(&cfg.Email)
			err := Validate(cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid config, got: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("Validate error = %v, want exactly %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateMaxBrowsers(t *testing.T) {
	tests := []struct {
		name         string
		workers      int
		browsers     int
		wantBrowsers int
		wantErr      string // empty means valid
	}{
		{"one", 4, 1, 1, ""},
		{"equal to max_workers", 4, 4, 4, ""},
		{"above max_workers", 4, 5, 5, "concurrency.max_browsers must not exceed concurrency.max_workers (4), got 5"},
		{"negative", 4, -1, -1, "concurrency.max_browsers must be at least 1, got -1"},
		{"unset defaults to max_workers", 4, 0, 4, ""},
		{"unset with one worker", 1, 0, 1, ""},
		{"unset with invalid max_workers", -1, 0, -1, "concurrency.max_workers must be at least 1, got -1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Email:       validEmail(),
				Concurrency: ConcurrencyConfig{MaxWorkers: tt.workers, MaxBrowsers: tt.browsers, AccountCount: 1},
			}
			applyDefaults(cfg)
			if cfg.Concurrency.MaxBrowsers != tt.wantBrowsers {
				t.Errorf("MaxBrowsers = %d, want %d", cfg.Concurrency.MaxBrowsers, tt.wantBrowsers)
			}
			err := Validate(cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid config, got: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("Validate error = %v, want exactly %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateMultipleErrors(t *testing.T) {
	cfg := &Config{
		Chrome:      ChromeConfig{Timeout: -1},
		Database:    "creator.db",
		Account:     AccountConfig{PasswordLength: 2},
		Email:       EmailConfig{IMAPPort: 993},
		Concurrency: ConcurrencyConfig{MaxWorkers: 0, AccountCount: -5},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected multiple validation errors")
	}
	// Should report all errors, not just the first one.
	errStr := err.Error()
	for _, want := range []string{"chrome.timeout", "password_length", "imap_host", "max_workers", "account_count"} {
		if !strings.Contains(errStr, want) {
			t.Errorf("expected error to mention %q, got: %v", want, err)
		}
	}
}

func TestLoadValidationError(t *testing.T) {
	// A YAML with bad values should fail Load even though it parses fine.
	_, err := Load(writeConfig(t, "account:\n  password_length: 2\n"+validIMAPYAML))
	if err == nil {
		t.Fatal("expected validation error from Load")
	}
	if !strings.Contains(err.Error(), "invalid config") {
		t.Errorf("expected 'invalid config' prefix, got: %v", err)
	}
}

func TestRead(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		overrides []func(*Config)
		check     func(*Config) string // returns the mismatch, or "" when cfg is right
	}{
		{
			name: "an empty file gives the defaults and no required-field error",
			check: func(c *Config) string {
				if c.Database != "creator.db" || c.Concurrency.MaxWorkers != 1 || c.Email.IMAPPort != 993 || c.Email.IMAPHost != "" {
					return fmt.Sprintf("database, max_workers, imap_port, imap_host = %q, %d, %d, %q", c.Database, c.Concurrency.MaxWorkers, c.Email.IMAPPort, c.Email.IMAPHost)
				}
				return ""
			},
		},
		{
			name: "a bare imap_domains key gives an empty non-nil list",
			yaml: "email:\n  imap_domains:\n",
			check: func(c *Config) string {
				if c.Email.IMAPDomains == nil || len(c.Email.IMAPDomains) != 0 {
					return fmt.Sprintf("imap_domains = %#v, want an empty non-nil list", c.Email.IMAPDomains)
				}
				return ""
			},
		},
		{
			name: "a list of domains stays in order",
			yaml: "email:\n  imap_domains: [b.example.com, a.example.com]\n",
			check: func(c *Config) string {
				if !slices.Equal(c.Email.IMAPDomains, []string{"b.example.com", "a.example.com"}) {
					return fmt.Sprintf("imap_domains = %q, want [b.example.com a.example.com]", c.Email.IMAPDomains)
				}
				return ""
			},
		},
		{
			name: "an invalid value stays",
			yaml: validIMAPYAML + "account:\n  password_length: 2\n",
			check: func(c *Config) string {
				if c.Account.PasswordLength != 2 {
					return fmt.Sprintf("password_length = %d, want 2", c.Account.PasswordLength)
				}
				return ""
			},
		},
		{
			name:      "an override runs after the defaults",
			yaml:      validIMAPYAML,
			overrides: []func(*Config){func(c *Config) { c.Concurrency.AccountCount = 0 }},
			check: func(c *Config) string {
				if c.Concurrency.AccountCount != 0 {
					return fmt.Sprintf("account_count = %d, want 0", c.Concurrency.AccountCount)
				}
				return ""
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Read(writeConfig(t, tt.yaml), tt.overrides...)
			if err != nil {
				t.Fatalf("Read error = %v, want nil", err)
			}
			if msg := tt.check(cfg); msg != "" {
				t.Error(msg)
			}
		})
	}
}

func TestReadErrors(t *testing.T) {
	for _, tt := range []struct{ name, path, wantPrefix string }{
		{"missing file", "", "reading config file: "},
		{"unknown key", writeConfig(t, "provider: imap\n"), "parsing config file: "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.path
			if path == "" {
				path = filepath.Join(t.TempDir(), "missing.yaml")
			}
			cfg, err := Read(path)
			if cfg != nil || err == nil || !strings.HasPrefix(err.Error(), tt.wantPrefix) {
				t.Fatalf("Read() = %v, %v, want nil and an error with the prefix %q", cfg, err, tt.wantPrefix)
			}
		})
	}
}
