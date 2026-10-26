package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	want, err := Read(writeConfig(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if got := Defaults(); !reflect.DeepEqual(got, want) {
		t.Errorf("Defaults() = %+v, want the Read of an empty file %+v", got, want)
	}
}

// savedConfig returns a config with no default value. The password needs quotes in YAML.
func savedConfig() *Config {
	return &Config{
		Debug:    true,
		Database: "creator.db",
		Chrome:   ChromeConfig{Headless: new(false), Timeout: 45 * time.Second},
		Proxy:    ProxyConfig{Direct: true},
		Email: EmailConfig{IMAPHost: "mail.example.com", IMAPPort: 143, IMAPUser: "123",
			IMAPPass: `p@ss: wo#rd "x" 'y'`, IMAPDomains: []string{"example.com", "example.org"}},
		Account:     AccountConfig{PasswordLength: 20, EnableTOTP: new(false)},
		Concurrency: ConcurrencyConfig{MaxWorkers: 4, MaxBrowsers: 2, AccountCount: -1, AccountTimeout: 90 * time.Second},
	}
}

// The YAML sections of savedConfig, as a save writes them.
const (
	savedTop    = "debug: true\ndatabase: creator.db\n"
	savedChrome = "chrome:\n  headless: false\n  timeout: 45s\n"
	savedProxy  = "proxy:\n  direct: true\n"
	savedEmail  = "  imap_host: mail.example.com\n  imap_port: 143\n  imap_user: \"123\"\n" +
		"  imap_pass: 'p@ss: wo#rd \"x\" ''y'''\n  imap_domains:\n    - example.com\n    - example.org\n"
	savedRest = "account:\n  password_length: 20\n  enable_totp: false\n" +
		"concurrency:\n  max_workers: 4\n  max_browsers: 2\n  account_count: -1\n  account_timeout: 1m30s\n"
	savedAll = savedTop + savedChrome + savedProxy + "email:\n" + savedEmail + savedRest
)

func TestSave(t *testing.T) {
	tests := []struct {
		name    string
		old     *string // nil means no file
		want    string
		wantErr string // the prefix of the error. Empty means no error
	}{
		{name: "a missing file gets the whole config", want: savedAll},
		{name: "an empty file gets the whole config", old: new(""), want: savedAll},
		{name: "a file of only comments keeps them", old: new("# one\n# two\n"), want: "# one\n# two\n" + savedAll},
		{name: "a comment with no newline at the end gets one", old: new("# one"), want: "# one\n" + savedAll},
		{
			name: "the comments and the key order stay, and a missing key goes at the end of its section",
			old:  new("# head\nemail: # the mailbox\n  # host comment\n  imap_host: old.example.com # host\ndebug: false # the debug flag\n"),
			want: "# head\nemail: # the mailbox\n  # host comment\n" + strings.Replace(savedEmail, "example.com\n", "example.com # host\n", 1) +
				"debug: true # the debug flag\ndatabase: creator.db\n" + savedChrome + savedProxy + savedRest,
		},
		{
			name: "a flow mapping stays a flow mapping",
			old:  new("chrome: {headless: true, timeout: 30s} # flow\n"),
			want: "chrome: {headless: false, timeout: 45s} # flow\n" + savedTop + savedProxy + "email:\n" + savedEmail + savedRest,
		},
		{
			name: "an empty section gets its keys and keeps its comment",
			old:  new("email: # the mailbox\n"),
			want: "email: # the mailbox\n" + savedEmail + savedTop + savedChrome + savedProxy + savedRest,
		},
		{
			name: "a list replaces the old list and keeps its comments",
			old:  new("email:\n  # the domains\n  imap_domains: # one at random\n    - old.example.com # old\n"),
			want: "email:\n  # the domains\n  imap_domains: # one at random\n    - example.com\n    - example.org\n" +
				strings.Replace(savedEmail, "  imap_domains:\n    - example.com\n    - example.org\n", "", 1) + savedTop + savedChrome + savedProxy + savedRest,
		},
		{name: "a null document gets the whole config", old: new("null\n"), want: savedAll},
		{name: "an unknown key is refused", old: new("provider: imap\n"), wantErr: "parsing config file: "},
		{name: "a wrong type is refused", old: new("email:\n  imap_port: '993'\n"), wantErr: "parsing config file: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.local.yaml")
			if tt.old != nil {
				if err := os.WriteFile(path, []byte(*tt.old), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			err := Save(path, savedConfig())

			if tt.wantErr != "" {
				if err == nil || !strings.HasPrefix(err.Error(), tt.wantErr) {
					t.Fatalf("Save error = %v, want the prefix %q", err, tt.wantErr)
				}
				assertFile(t, path, *tt.old)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			assertFile(t, path, tt.want)
			if cfg, err := Read(path); err != nil || !reflect.DeepEqual(cfg, savedConfig()) {
				t.Errorf("Read of the saved file = %+v, %v, want %+v", cfg, err, savedConfig())
			}
			if err := Save(path, savedConfig()); err != nil {
				t.Fatal(err)
			}
			assertFile(t, path, tt.want)
		})
	}
}

// assertFile fails the test when the file at path does not hold want, or when
// a temp file of a save stays beside it.
func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
	assertNoTemp(t, filepath.Dir(path))
}

// assertNoTemp fails the test when a temp file of a save stays in dir.
func assertNoTemp(t *testing.T, dir string) {
	t.Helper()
	left, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil || len(left) > 0 {
		t.Errorf("temp files left = %q, %v, want none", left, err)
	}
}

func TestSaveMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits do nothing on Windows")
	}
	path := filepath.Join(t.TempDir(), "config.local.yaml")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, savedConfig()); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("Stat = %v, %v, want mode 0600", info, err)
	}
}

func TestSaveErrors(t *testing.T) {
	t.Run("a dir at the path fails the read", func(t *testing.T) {
		dir := t.TempDir()
		err := Save(dir, savedConfig())
		if err == nil || !strings.HasPrefix(err.Error(), "reading config file: ") {
			t.Errorf("Save error = %v, want the prefix %q", err, "reading config file: ")
		}
	})
	t.Run("a missing dir fails the temp file", func(t *testing.T) {
		err := Save(filepath.Join(t.TempDir(), "missing", "config.local.yaml"), savedConfig())
		if err == nil || !strings.HasPrefix(err.Error(), "creating the temp config file: ") {
			t.Errorf("Save error = %v, want the prefix %q", err, "creating the temp config file: ")
		}
	})
	// Save reads the path first and fails there on a dir, so this case calls replaceFile.
	t.Run("a dir at the path fails the rename and the temp file goes", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.local.yaml")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		err := replaceFile(path, []byte(savedAll))
		if err == nil || !strings.HasPrefix(err.Error(), "replacing config file: ") {
			t.Errorf("replaceFile error = %v, want the prefix %q", err, "replacing config file: ")
		}
		assertNoTemp(t, dir)
	})
	t.Run("a failed write closes the file and renames nothing", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "src.tmp")
		if err := os.WriteFile(src, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(src) // read-only, so the write fails
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "config.local.yaml")

		err = commitTemp(f, path, []byte(savedAll))

		if err == nil || !strings.HasPrefix(err.Error(), "writing config file: ") {
			t.Errorf("commitTemp error = %v, want the prefix %q", err, "writing config file: ")
		}
		if err := f.Close(); !errors.Is(err, os.ErrClosed) {
			t.Errorf("a second Close = %v, want os.ErrClosed", err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Stat of the target = %v, want os.ErrNotExist", err)
		}
	})
}
