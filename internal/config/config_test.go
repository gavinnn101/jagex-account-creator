package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
			IMAPPort: 143,
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
const validIMAPYAML = "email:\n  imap_host: mail.example.com\n  imap_user: user\n  imap_pass: pass\n  imap_domain: example.com\n"

// validEmail is the struct form of validIMAPYAML.
func validEmail() EmailConfig {
	return EmailConfig{IMAPHost: "mail.example.com", IMAPUser: "user", IMAPPass: "pass", IMAPDomain: "example.com"}
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
	for _, want := range []string{"invalid config", "imap_host", "imap_user", "imap_pass", "imap_domain"} {
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

func TestValidateIMAPRequiredFields(t *testing.T) {
	tests := []struct {
		name string
		cfg  EmailConfig
		want string // substring expected in error
	}{
		{
			name: "missing host",
			cfg:  EmailConfig{IMAPUser: "u", IMAPPass: "p", IMAPDomain: "d"},
			want: "imap_host",
		},
		{
			name: "missing user",
			cfg:  EmailConfig{IMAPHost: "h", IMAPPass: "p", IMAPDomain: "d"},
			want: "imap_user",
		},
		{
			name: "missing pass",
			cfg:  EmailConfig{IMAPHost: "h", IMAPUser: "u", IMAPDomain: "d"},
			want: "imap_pass",
		},
		{
			name: "missing domain",
			cfg:  EmailConfig{IMAPHost: "h", IMAPUser: "u", IMAPPass: "p"},
			want: "imap_domain",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Email: tt.cfg}
			applyDefaults(cfg)
			cfg.Email = tt.cfg       // restore test values after defaults
			cfg.Email.IMAPPort = 993 // avoid port error
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
