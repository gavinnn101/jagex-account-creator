package main

import (
	"bytes"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"jagex_account_creator/internal/config"
	"jagex_account_creator/internal/logtest"
)

func TestParseFlags(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		wantPath     string
		wantMode     string
		wantHeadless bool
		wantDebug    bool
		wantCount    int
		wantOverride int
		wantErr      string
	}{
		{name: "no flags leave the config", args: nil, wantPath: "config.local.yaml", wantMode: "full", wantHeadless: false, wantDebug: true, wantCount: 10},
		{name: "config path and mode", args: []string{"-config", "local.yaml", "-mode", "challenge"}, wantPath: "local.yaml", wantMode: "challenge", wantHeadless: false, wantDebug: true, wantCount: 10},
		{name: "headless false", args: []string{"-headless=false"}, wantPath: "config.local.yaml", wantMode: "full", wantHeadless: false, wantDebug: true, wantCount: 10, wantOverride: 1},
		{name: "headless", args: []string{"-headless"}, wantPath: "config.local.yaml", wantMode: "full", wantHeadless: true, wantDebug: true, wantCount: 10, wantOverride: 1},
		{name: "debug false", args: []string{"-debug=false"}, wantPath: "config.local.yaml", wantMode: "full", wantHeadless: false, wantDebug: false, wantCount: 10, wantOverride: 1},
		{name: "count unlimited", args: []string{"-count", "-1"}, wantPath: "config.local.yaml", wantMode: "full", wantHeadless: false, wantDebug: true, wantCount: -1, wantOverride: 1},
		{name: "count zero", args: []string{"-count", "0"}, wantPath: "config.local.yaml", wantMode: "full", wantHeadless: false, wantDebug: true, wantCount: 0, wantOverride: 1},
		{name: "bad boolean", args: []string{"-headless=maybe"}, wantErr: `invalid boolean value "maybe" for -headless: invalid syntax`},
		{name: "bad count", args: []string{"-count", "x"}, wantErr: `invalid value "x" for flag -count: invalid syntax`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := parseFlags(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.HasSuffix(err.Error(), tt.wantErr) {
					t.Fatalf("parseFlags error = %v, want it to end with %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if opts.configPath != tt.wantPath || opts.mode != tt.wantMode {
				t.Errorf("config, mode = %q, %q, want %q, %q", opts.configPath, opts.mode, tt.wantPath, tt.wantMode)
			}
			if len(opts.overrides) != tt.wantOverride {
				t.Errorf("got %d overrides, want %d", len(opts.overrides), tt.wantOverride)
			}

			cfg := &config.Config{Debug: true, Chrome: config.ChromeConfig{Headless: new(false)}}
			cfg.Concurrency.AccountCount = 10
			for _, override := range opts.overrides {
				override(cfg)
			}
			if *cfg.Chrome.Headless != tt.wantHeadless || cfg.Debug != tt.wantDebug || cfg.Concurrency.AccountCount != tt.wantCount {
				t.Errorf("headless, debug, count = %v, %v, %d, want %v, %v, %d",
					*cfg.Chrome.Headless, cfg.Debug, cfg.Concurrency.AccountCount,
					tt.wantHeadless, tt.wantDebug, tt.wantCount)
			}
		})
	}
}

func TestParseFlagsHelp(t *testing.T) {
	if _, err := parseFlags([]string{"-help"}); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("parseFlags error = %v, want flag.ErrHelp", err)
	}
}

func TestParseFlagsCountZeroFailsLoad(t *testing.T) {
	opts, err := parseFlags([]string{"-count", "0"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "email:\n  imap_host: mail.example.com\n  imap_user: user\n  imap_pass: pass\n  imap_domains: [example.com]\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = config.Load(path, opts.overrides...)
	const want = "concurrency.account_count must be positive or -1 (unlimited), got 0"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("Load error = %v, want it to contain %q", err, want)
	}
}

func TestParseFlagsListen(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want string
	}{
		{nil, "127.0.0.1:8080"},
		{[]string{"-mode", "serve", "-listen", "[::1]:9000"}, "[::1]:9000"},
	} {
		opts, err := parseFlags(tt.args)
		if err != nil || opts.listen != tt.want {
			t.Errorf("parseFlags(%q): listen = %q, error = %v, want %q", tt.args, opts.listen, err, tt.want)
		}
	}
}

// An empty file fails the validation, so only serve mode starts on it. Only
// serve mode starts on a missing file, and it refuses the config flags.
func TestLoadConfig(t *testing.T) {
	const (
		valid  = "email:\n  imap_host: mail.example.com\n  imap_user: user\n  imap_pass: pass\n  imap_domains: [example.com]\n"
		loaded = `level=INFO msg="config loaded" file=config.local.yaml` + "\n"
	)
	count := []func(*config.Config){func(cfg *config.Config) { cfg.Concurrency.AccountCount = 7 }}
	withEmail := config.Defaults()
	withEmail.Email = config.EmailConfig{IMAPHost: "mail.example.com", IMAPPort: 993, IMAPUser: "user", IMAPPass: "pass", IMAPDomains: []string{"example.com"}, Auth: config.AuthPassword}
	withEmail.Concurrency.AccountCount = 7
	tests := []struct {
		name, mode, content string
		missing             bool
		overrides           []func(*config.Config)
		want                *config.Config
		wantErr             string // a part of the error text. Empty means no error
		wantNotExist        bool
		wantLog             string
	}{
		{name: "serve on an empty file", mode: "serve", want: config.Defaults(), wantLog: loaded},
		{name: "serve on a missing file", mode: "serve", missing: true, want: config.Defaults(),
			wantLog: `level=INFO msg="config file not found, serving the defaults until the first save creates it" file=config.local.yaml` + "\n"},
		{name: "serve refuses a config flag", mode: "serve", content: valid, overrides: count,
			wantErr: "serve mode refuses -headless, -debug and -count: a run of serve takes its config from the dashboard"},
		{name: "full on a valid file with a flag", mode: "full", content: valid, overrides: count, want: withEmail, wantLog: loaded},
		{name: "full on an empty file", mode: "full", wantErr: "invalid config: "},
		{name: "challenge on an empty file", mode: "challenge", wantErr: "invalid config: "},
		{name: "export on an empty file", mode: "export", wantErr: "invalid config: "},
		{name: "full on a missing file", mode: "full", missing: true, wantErr: " (copy the template config.yaml to config.local.yaml)", wantNotExist: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if !tt.missing {
				if err := os.WriteFile("config.local.yaml", []byte(tt.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var buf bytes.Buffer

			cfg, err := loadConfig(logtest.New(&buf), cliOptions{mode: tt.mode, configPath: "config.local.yaml", overrides: tt.overrides})

			if tt.wantErr == "" && (err != nil || !reflect.DeepEqual(cfg, tt.want)) {
				t.Errorf("loadConfig() = %+v, %v, want %+v and no error", cfg, err, tt.want)
			}
			if tt.wantErr != "" && (cfg != nil || err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Errorf("loadConfig() = %+v, %v, want nil and an error that holds %q", cfg, err, tt.wantErr)
			}
			if errors.Is(err, fs.ErrNotExist) != tt.wantNotExist {
				t.Errorf("errors.Is(%v, fs.ErrNotExist) = %v, want %v", err, !tt.wantNotExist, tt.wantNotExist)
			}
			if buf.String() != tt.wantLog {
				t.Errorf("log = %q, want %q", buf.String(), tt.wantLog)
			}
		})
	}
}

func TestStrconvCause(t *testing.T) {
	other := errors.New("other")
	if got := strconvCause(other); !errors.Is(got, other) {
		t.Errorf("strconvCause(other) = %v, want other unchanged", got)
	}
}
