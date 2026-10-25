package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"jagex_account_creator/internal/logtest"
)

func TestKillBeforeLaunch(_ *testing.T) {
	l := NewLauncher(slog.Default(), nil)
	// No Launch ran, so there is no watch to stop. Kill must not panic.
	l.Kill()
	l.Kill()
}

func TestKillStopsTheWatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLauncher(slog.Default(), nil)
		ctx, cancel := context.WithCancel(t.Context())
		fired := false
		l.stopWatch = context.AfterFunc(ctx, func() { fired = true })

		l.Kill()
		cancel()
		synctest.Wait()

		if l.stopWatch != nil || fired {
			t.Errorf("after Kill and the end of ctx: watch kept = %v, watch ran = %v, want neither", l.stopWatch != nil, fired)
		}
	})
}

func TestStop(t *testing.T) {
	methods := []struct {
		name string
		stop func(*Launcher)
	}{
		{name: "Kill", stop: (*Launcher).Kill},
		{name: "Shutdown", stop: func(l *Launcher) { l.Shutdown(t.Context(), nil) }},
	}
	for _, m := range methods {
		t.Run(m.name+" removes the profile dir and frees the cache dir", func(t *testing.T) {
			cm := newTestCacheManager(t)
			l := NewLauncher(slog.Default(), cm)
			l.userDataDir = t.TempDir()
			l.lease = mustAcquire(t, cm)
			profileDir, cacheDir := l.userDataDir, l.lease.dir

			m.stop(l)

			if _, err := os.Stat(profileDir); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("Stat(%q) error = %v, want not-exist after %s", profileDir, err, m.name)
			}
			if _, err := os.Stat(cacheDir); err != nil {
				t.Errorf("Stat(%q) error = %v, want the cache dir kept after %s", cacheDir, err, m.name)
			}
			if l.userDataDir != "" || l.lease != (cacheLease{}) {
				t.Errorf("after %s profile = %q and lease = %+v, want both empty", m.name, l.userDataDir, l.lease)
			}
			wantLease(t, cm, mustAcquire(t, cm), 0)
		})
		t.Run(m.name+" skips an unstarted command", func(t *testing.T) {
			cm := newTestCacheManager(t)
			l := NewLauncher(slog.Default(), cm)
			// A command with no process has nothing to stop. The stop must not read its pid.
			l.cmd = &exec.Cmd{}
			// Launch stores the lease before cmd.Start, so a failed start still frees it.
			l.lease = mustAcquire(t, cm)

			m.stop(l)

			wantLease(t, cm, mustAcquire(t, cm), 0)
		})
	}
}

func TestAwaitExit(t *testing.T) {
	const killToExit = 500 * time.Millisecond // the time the killed process takes to exit
	tests := []struct {
		name     string
		exitAt   time.Duration // the graceful exit, or 0 for none
		cancelAt time.Duration // the end of ctx, or 0 for none
		wantKill time.Duration // the time of the one kill, or 0 for none
		wantLog  string
	}{
		{name: "graceful exit", exitAt: 2 * time.Second, wantLog: `msg="chrome exited gracefully"`},
		{name: "the grace ends", wantKill: 10 * time.Second, wantLog: `msg="chrome did not exit in time, killing"`},
		{name: "ctx ends", cancelAt: time.Second, wantKill: time.Second, wantLog: `msg="stopping at once, killing chrome"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var buf bytes.Buffer
				l := NewLauncher(logtest.New(&buf), nil)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				start := time.Now()
				exited := make(chan struct{})
				if tt.exitAt > 0 {
					time.AfterFunc(tt.exitAt, func() { close(exited) })
				}
				if tt.cancelAt > 0 {
					time.AfterFunc(tt.cancelAt, cancel)
				}
				var kills []time.Duration
				kill := func() {
					kills = append(kills, time.Since(start))
					time.AfterFunc(killToExit, func() { close(exited) })
				}

				l.awaitExit(ctx, exited, kill, 10*time.Second)
				took := time.Since(start)

				wantKills, wantTook := []time.Duration(nil), tt.exitAt
				if tt.wantKill > 0 {
					wantKills, wantTook = []time.Duration{tt.wantKill}, tt.wantKill+killToExit
				}
				if !slices.Equal(kills, wantKills) {
					t.Errorf("kills at %v, want %v", kills, wantKills)
				}
				if took != wantTook {
					t.Errorf("awaitExit took %v, want exactly %v: it returns only after the exit", took, wantTook)
				}
				if !strings.Contains(buf.String(), tt.wantLog) || strings.Count(buf.String(), "\n") != 1 {
					t.Errorf("log = %q, want one line with %s", buf.String(), tt.wantLog)
				}
			})
		})
	}
}

func TestChromeArgs(t *testing.T) {
	base := []string{
		"--remote-debugging-port=0",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-default-apps",
		"--disable-extensions",
		"--disable-sync",
		"--disable-translate",
		"--disable-background-networking",
		"--disable-client-side-phishing-detection",
		"--disable-hang-monitor",
		"--disable-popup-blocking",
		"--disable-prompt-on-repost",
		"--metrics-recording-only",
		"--safebrowsing-disable-auto-update",
		"--password-store=basic",
		"--use-mock-keychain",
		"--no-sandbox",
		"--disable-blink-features=AutomationControlled",
		"--disable-features=PrivacySandboxSettings4,OptimizationGuideModelDownloading,OptimizationHintsFetching,OptimizationTargetPrediction,OptimizationHints,CriticalClientHint",
		"--disable-component-update",
		"--disable-background-downloads",
		"--disable-crash-reporter",
		"--accept-lang=en-US",
		"--window-size=1920,1080",
		"--user-data-dir=/profile",
	}
	tests := []struct {
		name         string
		diskCacheDir string
		headless     bool
		proxy        string
		wantTail     []string
	}{
		{name: "no options", wantTail: []string{"about:blank"}},
		{
			name:         "all options",
			diskCacheDir: "/cache",
			headless:     true,
			proxy:        "127.0.0.1:8080",
			wantTail:     []string{"--disk-cache-dir=/cache", "--headless=new", "--proxy-server=127.0.0.1:8080", "about:blank"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chromeArgs("/profile", tt.diskCacheDir, tt.headless, tt.proxy)
			if want := slices.Concat(base, tt.wantTail); !slices.Equal(got, want) {
				t.Errorf("chromeArgs() = %q, want %q", got, want)
			}
		})
	}
}

func TestAwaitDevToolsURL(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		wantOK bool
		want   string
	}{
		{
			name:   "URL after other lines",
			stderr: "starting\nDevTools listening on ws://127.0.0.1:9222/devtools/browser/abc\nDevTools listening on ws://second\n",
			wantOK: true,
			want:   "ws://127.0.0.1:9222/devtools/browser/abc",
		},
		{name: "exit before URL", stderr: "starting\nDevTools listening on\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := NewLauncher(slog.Default(), nil)

			ok := l.awaitDevToolsURL(strings.NewReader(tt.stderr))

			if ok != tt.wantOK || l.WebSocketURL() != tt.want {
				t.Errorf("awaitDevToolsURL() = %v with URL %q, want %v with %q", ok, l.WebSocketURL(), tt.wantOK, tt.want)
			}
		})
	}
}

// --- UA cache tests ---

// setCacheEnv sets every variable that os.UserCacheDir reads to dir.
func setCacheEnv(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"LOCALAPPDATA", "XDG_CACHE_HOME", "HOME"} {
		t.Setenv(name, dir)
	}
}

// isolateUACache points the user cache directory at a temp directory, so a
// test never reads or replaces the real UA cache.
func isolateUACache(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	setCacheEnv(t, dir)
	cachePath, err := uaCachePath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cachePath, dir) {
		t.Fatalf("uaCachePath() = %q, want a path under %q", cachePath, dir)
	}
}

func TestLoadUACacheMissWhenNoFile(t *testing.T) {
	isolateUACache(t)
	// Use a temp file as the "Chrome binary" so Stat works.
	tmp := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(tmp, []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	// No cache file exists → should miss.
	_, ok := loadUACache(tmp, "145.0.0.0")
	if ok {
		t.Fatal("expected cache miss when no cache file exists")
	}
}

func TestSaveAndLoadUACache(t *testing.T) {
	isolateUACache(t)
	// Create a fake Chrome binary.
	chromeBin := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(chromeBin, []byte("fake-chrome"), 0755); err != nil {
		t.Fatal(err)
	}

	version := "145.0.0.0"
	want := testOverride("Mozilla/5.0 Round Trip UA")
	if err := saveUACache(chromeBin, version, want); err != nil {
		t.Fatal(err)
	}

	got, ok := loadUACache(chromeBin, version)
	if !ok {
		t.Fatal("expected cache hit after saveUACache")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cached override = %+v, want %+v", got, want)
	}
}

// writeUACacheFile writes data as the UA cache file.
func writeUACacheFile(t *testing.T, data []byte) {
	t.Helper()
	cachePath, err := uaCachePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, data, 0644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadUACacheOldFormat checks that an entry with no metadata misses, so the
// caller detects again, and that the entry saved after the detection hits.
func TestLoadUACacheOldFormat(t *testing.T) {
	isolateUACache(t)
	chromeBin := filepath.Join(t.TempDir(), "chrome")
	version := "155.0.8059.40"
	old, err := json.Marshal(map[string]string{"chrome_path": chromeBin, "chrome_version": version, "user_agent": "old UA"})
	if err != nil {
		t.Fatal(err)
	}
	writeUACacheFile(t, old)

	if _, ok := loadUACache(chromeBin, version); ok {
		t.Fatal("expected cache miss for an entry with no metadata")
	}

	want := testOverride("new UA")
	if err := saveUACache(chromeBin, version, want); err != nil {
		t.Fatal(err)
	}
	got, ok := loadUACache(chromeBin, version)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("loadUACache() after the new save = %+v, %v, want %+v, true", got, ok, want)
	}
}

func TestLoadUACacheMissEmptyBrands(t *testing.T) {
	isolateUACache(t)
	chromeBin := filepath.Join(t.TempDir(), "chrome")
	data, err := json.Marshal(uaCache{ChromePath: chromeBin, ChromeVersion: "145.0.0.0", UserAgent: "UA", Metadata: &UserAgentMetadata{}})
	if err != nil {
		t.Fatal(err)
	}
	writeUACacheFile(t, data)

	if _, ok := loadUACache(chromeBin, "145.0.0.0"); ok {
		t.Fatal("expected cache miss for metadata with no brands")
	}
}

func TestLoadUACacheMissAfterVersionChange(t *testing.T) {
	isolateUACache(t)
	chromeBin := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(chromeBin, []byte("v1"), 0755); err != nil {
		t.Fatal(err)
	}

	// Save cache with one version.
	if err := saveUACache(chromeBin, "145.0.0.0", testOverride("old UA")); err != nil {
		t.Fatal(err)
	}

	// Load with a different version → should miss.
	_, ok := loadUACache(chromeBin, "146.0.0.0")
	if ok {
		t.Fatal("expected cache miss after Chrome version changed")
	}
}

func TestLoadUACacheMissWrongPath(t *testing.T) {
	isolateUACache(t)
	chromeBin := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(chromeBin, []byte("v1"), 0755); err != nil {
		t.Fatal(err)
	}

	version := "145.0.0.0"
	if err := saveUACache(chromeBin, version, testOverride("cached UA")); err != nil {
		t.Fatal(err)
	}

	// Load with a different path.
	otherBin := filepath.Join(t.TempDir(), "chromium")
	if err := os.WriteFile(otherBin, []byte("v1"), 0755); err != nil {
		t.Fatal(err)
	}

	_, ok := loadUACache(otherBin, version)
	if ok {
		t.Fatal("expected cache miss when chrome path differs")
	}
}

func TestLoadUACacheMissCorruptFile(t *testing.T) {
	isolateUACache(t)
	chromeBin := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(chromeBin, []byte("v1"), 0755); err != nil {
		t.Fatal(err)
	}

	// Write corrupt data to the cache file.
	cachePath, err := uaCachePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}

	_, ok := loadUACache(chromeBin, "145.0.0.0")
	if ok {
		t.Fatal("expected cache miss on corrupt cache file")
	}
}

func TestLoadUACacheMissEmptyUA(t *testing.T) {
	isolateUACache(t)
	chromeBin := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(chromeBin, []byte("v1"), 0755); err != nil {
		t.Fatal(err)
	}

	// Write cache with empty UA. The metadata is valid, so only the UA can miss.
	metadata := measuredMetadata()
	cache := uaCache{
		ChromePath:    chromeBin,
		ChromeVersion: "145.0.0.0",
		UserAgent:     "",
		Metadata:      &metadata,
	}
	data, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	writeUACacheFile(t, data)

	_, ok := loadUACache(chromeBin, "145.0.0.0")
	if ok {
		t.Fatal("expected cache miss when stored UA is empty")
	}
}

func TestParseChromeVersion(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "bare version", raw: "155.0.8059.40", want: "155.0.8059.40"},
		{name: "product name and trailing space", raw: "Google Chrome 155.0.8059.40 ", want: "155.0.8059.40"},
		{name: "snap suffix", raw: "Chromium 145.0.6422.60 snap", want: "145.0.6422.60"},
		{name: "product name", raw: "Chromium 145.0.0.0", want: "145.0.0.0"},
		{name: "handoff text", raw: "Opening in existing browser session.", wantErr: true},
		{name: "empty", raw: "", wantErr: true},
		{name: "three parts", raw: "Google Chrome 155.0.8059", wantErr: true},
		{name: "five parts", raw: "155.0.0.0.1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseChromeVersion(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseChromeVersion(%q) error = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("parseChromeVersion(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// setChromePath points CHROME_PATH at path and checks that findChrome picks it.
// A broken env check then fails before the test runs the host's Chrome, which can hang.
func setChromePath(t *testing.T, path string) {
	t.Helper()
	t.Setenv("CHROME_PATH", path)
	if got, err := findChrome(); err != nil || got != path {
		t.Fatalf("findChrome = %q, %v, want %q", got, err, path)
	}
}

func TestInstalledChromeRejectsJunkBinary(t *testing.T) {
	// A 0644 file has no version resource on Windows, and exec refuses it on Unix.
	junk := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(junk, []byte("not a browser"), 0644); err != nil {
		t.Fatal(err)
	}
	setChromePath(t, junk)
	if bin, err := InstalledChrome(t.Context()); err == nil {
		t.Fatalf("InstalledChrome = %+v, want an error", bin)
	}
}

func TestDetectUserAgentCacheHit(t *testing.T) {
	isolateUACache(t)
	// A miss would launch CHROME_PATH, and a junk binary fails that launch at once.
	junk := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(junk, []byte("not a browser"), 0644); err != nil {
		t.Fatal(err)
	}
	setChromePath(t, junk)
	bin := ChromeBinary{Path: junk, Version: "145.0.0.0"}
	want := testOverride("Mozilla/5.0 Cached UA")
	if err := saveUACache(bin.Path, bin.Version, want); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer

	got, err := DetectUserAgent(t.Context(), logtest.New(&buf), bin)

	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("DetectUserAgent() = %+v, %v, want %+v", got, err, want)
	}
	if line := `level=DEBUG msg="using cached user-agent" phase=ua-detect ua="Mozilla/5.0 Cached UA"` + "\n"; buf.String() != line {
		t.Errorf("log = %q, want %q", buf.String(), line)
	}
}

// TestFindChromeFixedPaths checks findChrome against the fixed paths of this host,
// because a test cannot create or remove them.
func TestFindChromeFixedPaths(t *testing.T) {
	t.Setenv("CHROME_PATH", "")
	t.Setenv("PATH", "")

	p, err := findChrome()

	if err == nil {
		if _, statErr := os.Stat(p); statErr != nil {
			t.Fatalf("findChrome() = %q, a path that does not exist: %v", p, statErr)
		}
		return
	}
	_, tried, _ := strings.Cut(err.Error(), "(tried: ")
	for c := range strings.SplitSeq(strings.TrimSuffix(tried, ")"), ", ") {
		if _, statErr := os.Stat(c); c != "" && statErr == nil {
			t.Errorf("findChrome() error = %v, but %s exists", err, c)
		}
	}
}

// plainFile writes a regular file that is not a program and returns its path.
func plainFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(path, []byte("not a browser"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// hideChrome clears each place that findChrome reads. It skips the test when a
// Chrome at a fixed path stays visible, because the test cannot remove it.
func hideChrome(t *testing.T) {
	t.Helper()
	for _, name := range []string{"CHROME_PATH", "PATH", "PROGRAMFILES", "PROGRAMFILES(X86)", "LOCALAPPDATA"} {
		t.Setenv(name, "")
	}
	if p, err := findChrome(); err == nil {
		t.Skipf("findChrome found %s at a fixed path", p)
	}
}

// readOnlyTempBase makes the temp base of Chrome a dir that refuses new entries.
// It skips the test when the OS still lets this user write there, as for root or on Windows.
func readOnlyTempBase(t *testing.T) {
	t.Helper()
	setChromePath(t, plainFile(t))
	setCacheEnv(t, t.TempDir())
	base, err := chromeTempBase()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "probe"), 0o755); err == nil {
		t.Skip("the OS let this user create a dir in a read-only dir")
	}
}

func TestLaunchSetupFailure(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T)
		want  string // the start of the error text
	}{
		{name: "no chrome", setup: hideChrome, want: "finding chrome: "},
		{
			name: "the temp base is under a file",
			setup: func(t *testing.T) {
				t.Helper()
				setChromePath(t, plainFile(t))
				setCacheEnv(t, plainFile(t))
			},
			want: "creating temp base dir: ",
		},
		{name: "the temp base is read-only", setup: readOnlyTempBase, want: "creating temp user-data-dir: "},
		{
			name: "chrome does not start",
			setup: func(t *testing.T) {
				t.Helper()
				setChromePath(t, plainFile(t))
				setCacheEnv(t, t.TempDir())
			},
			want: "starting chrome: ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t)
			l := NewLauncher(slog.Default(), nil)
			defer l.Kill()

			err := l.Launch(t.Context(), true, "")

			if err == nil || !strings.HasPrefix(err.Error(), tt.want) {
				t.Errorf("Launch() error = %v, want an error that starts with %q", err, tt.want)
			}
		})
	}
}

// fakeChromeEnv makes the test binary act as Chrome. TestMain reads it before any test runs.
const fakeChromeEnv = "BROWSER_TEST_FAKE_CHROME"

// fakeChildPIDEnv names the file where the "group" fake writes the pid of its child.
const fakeChildPIDEnv = "BROWSER_TEST_FAKE_CHILD_PID"

// fakeDevToolsURL is the URL that the "url" fake prints.
const fakeDevToolsURL = "ws://127.0.0.1:1/devtools/browser/fake"

func TestMain(m *testing.M) {
	switch os.Getenv(fakeChromeEnv) {
	case "group":
		// The fake starts a child in its own process group, like a Chrome renderer. Its pid goes to the file that fakeChildPIDEnv names.
		exe, err := os.Executable()
		if err != nil {
			os.Exit(3)
		}
		child := exec.Command(exe)
		child.Env = append(os.Environ(), fakeChromeEnv+"=hang")
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		if err := os.WriteFile(os.Getenv(fakeChildPIDEnv), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			os.Exit(3)
		}
		fmt.Fprintln(os.Stderr, "DevTools listening on "+fakeDevToolsURL)
		time.Sleep(time.Minute)
		os.Exit(0)
	case "url":
		fmt.Fprintln(os.Stderr, "DevTools listening on "+fakeDevToolsURL)
		time.Sleep(time.Minute)
		os.Exit(0)
	case "hang":
		time.Sleep(time.Minute)
		os.Exit(0)
	case "exit":
		fmt.Fprintln(os.Stderr, "starting")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// launchFake runs Launch on the test binary as Chrome in mode, under a 2s guard.
func launchFake(ctx context.Context, t *testing.T, l *Launcher, mode string) error {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	setCacheEnv(t, t.TempDir())
	setChromePath(t, exe)
	t.Setenv(fakeChromeEnv, mode)
	errc := make(chan error, 1)
	go func() { errc <- l.Launch(ctx, true, "") }()
	select {
	case err = <-errc:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("Launch did not return")
		return nil
	}
}

func TestLaunch(t *testing.T) {
	t.Run("takes a cache dir, and Kill frees it", func(t *testing.T) {
		cm := newTestCacheManager(t)
		l := NewLauncher(slog.Default(), cm)
		defer l.Kill()

		if err := launchFake(t.Context(), t, l, "url"); err != nil || l.WebSocketURL() != fakeDevToolsURL {
			t.Fatalf("Launch() = %v with URL %q, want nil with %q", err, l.WebSocketURL(), fakeDevToolsURL)
		}
		if l.lease == (cacheLease{}) {
			t.Error("Launch took no cache dir")
		}
		l.Kill()
		wantLease(t, cm, mustAcquire(t, cm), 0)
	})
	t.Run("runs with no cache dir when the pool fails", func(t *testing.T) {
		cm, err := NewCacheManager(filepath.Join(plainFile(t), "browser-cache"), slog.Default())
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		l := NewLauncher(logtest.New(&buf), cm)
		defer l.Kill()

		if err := launchFake(t.Context(), t, l, "url"); err != nil {
			t.Fatalf("Launch() = %v, want nil", err)
		}
		if warn := `level=WARN msg="failed to acquire browser cache dir"`; l.lease != (cacheLease{}) || !strings.Contains(buf.String(), warn) {
			t.Errorf("lease = %+v and log = %q, want no lease and the line %s", l.lease, buf.String(), warn)
		}
	})
}

func TestLaunchFailure(t *testing.T) {
	tests := []struct {
		name   string
		mode   string
		cancel bool
		want   string
	}{
		{name: "ctx ends before the URL", mode: "hang", cancel: true, want: "waiting for the devtools URL: context canceled"},
		{name: "chrome exits before the URL", mode: "exit", want: "chrome exited before providing devtools URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tt.cancel {
				cancel()
			}
			l := NewLauncher(slog.Default(), nil)
			defer l.Kill()

			err := launchFake(ctx, t, l, tt.mode)

			if err == nil || err.Error() != tt.want || errors.Is(err, context.Canceled) != tt.cancel {
				t.Errorf("Launch() error = %v, want %q (wraps context.Canceled: %v)", err, tt.want, tt.cancel)
			}
		})
	}
}
