package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/mafredri/cdp"

	"jagex_account_creator/internal/useragent"
)

// Launcher manages the Chrome process lifecycle.
type Launcher struct {
	cmd         *exec.Cmd
	wsURL       string
	userDataDir string
	lease       cacheLease  // the --disk-cache-dir from cache. Its dir is empty when Launch took none
	stopWatch   func() bool // ends the kill that Launch tied to the end of its ctx
	mu          sync.Mutex
	logger      *slog.Logger
	cache       *CacheManager
}

// NewLauncher returns a Launcher. Launch starts its Chrome.
// If cache is non-nil, Launch takes a disk cache dir from it, and Kill or
// Shutdown releases the dir. With a nil cache, Chrome keeps no disk cache.
func NewLauncher(logger *slog.Logger, cache *CacheManager) *Launcher {
	return &Launcher{
		logger: logger.With("component", "launcher"),
		cache:  cache,
	}
}

// Launch starts a Chrome process and blocks until the DevTools WebSocket URL
// is available. Kill ends the process when ctx ends. When ctx ends before the
// URL, the error wraps the cause of ctx.
// Chrome keeps its own UA, because --user-agent empties the high-entropy client
// hints. A headless run sets the UA with SetUserAgentOverride and TargetAttacher.
func (l *Launcher) Launch(ctx context.Context, headless bool, proxy string) error {
	chromePath, err := findChrome()
	if err != nil {
		return fmt.Errorf("finding chrome: %w", err)
	}
	l.logger.Debug("found chrome", "file", chromePath)

	// Each launch gets a fresh user-data-dir.
	tempBase, err := chromeTempBase()
	if err != nil {
		return fmt.Errorf("creating temp base dir: %w", err)
	}
	userDataDir, err := os.MkdirTemp(tempBase, "chrome-profile-*")
	if err != nil {
		return fmt.Errorf("creating temp user-data-dir: %w", err)
	}
	l.userDataDir = userDataDir

	// Chrome keeps its HTTP cache in a pooled dir, so a later Chrome on that dir
	// reads the hits. Launch stores the lease before any later return, so stop frees it.
	if l.cache != nil {
		lease, err := l.cache.acquire()
		if err != nil {
			l.logger.Warn("failed to acquire browser cache dir", "error", err)
		} else {
			l.lease = lease
		}
	}

	args := chromeArgs(userDataDir, l.lease.dir, headless, proxy)

	// Launch owns the one cancel path: Kill stops the process, releases the cache dir
	// and removes the profile dir.
	//nolint:noctx // A context watcher in exec would add a second kill that races Kill.
	cmd := exec.Command(chromePath, args...)
	// On Unix, Chrome and its child processes get their own process group, so
	// killGroup ends them all at once and leaves no orphaned child processes.
	setProcGroup(cmd)
	// Chrome writes the WS URL to stderr.
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("creating stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting chrome: %w", err)
	}
	l.logger.Debug("chrome started", "pid", cmd.Process.Pid)

	l.mu.Lock()
	l.cmd = cmd
	l.stopWatch = context.AfterFunc(ctx, l.Kill)
	l.mu.Unlock()

	if l.awaitDevToolsURL(stderrPipe) {
		return nil
	}
	// stop is the one caller of Wait, so this Kill and the one that ctx starts do not race.
	l.Kill()
	if ctx.Err() != nil {
		return fmt.Errorf("waiting for the devtools URL: %w", context.Cause(ctx))
	}
	return errors.New("chrome exited before providing devtools URL")
}

// chromeArgs returns the Chrome command line for one launch, without the binary.
func chromeArgs(userDataDir, diskCacheDir string, headless bool, proxy string) []string {
	args := []string{
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
		// Cloudflare's challenge 403 carries Critical-CH. With CriticalClientHint on, Chrome
		// cancels the first load and retries it, so the challenge page downloads two times.
		"--disable-features=PrivacySandboxSettings4,OptimizationGuideModelDownloading,OptimizationHintsFetching,OptimizationTargetPrediction,OptimizationHints,CriticalClientHint",
		"--disable-component-update",
		"--disable-background-downloads",
		"--disable-crash-reporter",
		// The HTTP client sends the accept-language that Chrome derives from this flag.
		"--accept-lang=" + useragent.BrowserLanguage,
		"--window-size=1920,1080",
		"--user-data-dir=" + userDataDir,
	}
	if diskCacheDir != "" {
		args = append(args, "--disk-cache-dir="+diskCacheDir)
	}
	if headless {
		args = append(args, "--headless=new")
	}
	if proxy != "" {
		args = append(args, "--proxy-server="+proxy)
	}

	// Start on about:blank so Chrome does not load its New Tab Page and its resources.
	return append(args, "about:blank")
}

// awaitDevToolsURL parses Chrome's stderr for the WebSocket URL and stores it.
// It returns false when r ends before Chrome prints the URL.
func (l *Launcher) awaitDevToolsURL(r io.Reader) bool {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		l.logger.Debug("chrome stderr", "line", line)
		if m := devToolsURLRe.FindStringSubmatch(line); len(m) == 2 {
			l.mu.Lock()
			l.wsURL = m[1]
			l.mu.Unlock()
			l.logger.Debug("got devtools url", "url", m[1])
			return true
		}
	}
	return false
}

// WebSocketURL returns the DevTools WebSocket debugger URL.
func (l *Launcher) WebSocketURL() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.wsURL
}

// Shutdown closes Chrome over CDP with Browser.close, so Chrome can flush its
// caches. If the process does not exit within 10 seconds, Shutdown calls killGroup.
// When ctx ends, Shutdown kills Chrome at once instead of waiting for the graceful exit.
// Then it releases the cache dir and removes the profile dir.
// cdpClient can be nil. Then Shutdown skips the CDP close.
func (l *Launcher) Shutdown(ctx context.Context, cdpClient *cdp.Client) {
	l.stop(func(cmd *exec.Cmd) { l.closeGracefully(ctx, cmd, cdpClient) })
}

// closeGracefully asks Chrome to close over CDP and waits for the exit.
func (l *Launcher) closeGracefully(ctx context.Context, cmd *exec.Cmd, cdpClient *cdp.Client) { // coverage-ignore: needs a live Chrome
	// Try graceful shutdown via CDP — this lets Chrome flush disk caches.
	if cdpClient != nil {
		closeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_ = cdpClient.Browser.Close(closeCtx)
		cancel()
	}

	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	// The 10-second limit gives Chrome time to write its disk cache, which can be slow.
	l.awaitExit(ctx, exited, func() { killGroup(cmd) }, 10*time.Second)
}

// awaitExit waits until exited closes. When grace passes or ctx ends first, it
// calls kill and then waits for exited, so Chrome never outlives the call.
func (l *Launcher) awaitExit(ctx context.Context, exited <-chan struct{}, kill func(), grace time.Duration) {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-exited:
		l.logger.Debug("chrome exited gracefully")
		return
	case <-timer.C:
		l.logger.Debug("chrome did not exit in time, killing")
	case <-ctx.Done():
		l.logger.Debug("stopping at once, killing chrome")
	}
	kill()
	<-exited
}

// Kill stops the Chrome process immediately, then releases the cache dir
// and removes the profile dir.
// Prefer Shutdown() when possible to allow cache flushing.
// Safe to call multiple times.
func (l *Launcher) Kill() {
	l.stop(func(cmd *exec.Cmd) {
		l.logger.Debug("killing chrome process group", "pid", cmd.Process.Pid)
		killGroup(cmd)
		_ = cmd.Wait()
	})
}

// stop ends a started Chrome process with endProcess, then releases the cache
// dir and removes the profile dir. Safe to call multiple times.
func (l *Launcher) stop(endProcess func(*exec.Cmd)) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.stopWatch != nil {
		l.stopWatch()
		l.stopWatch = nil
	}

	if l.cmd != nil && l.cmd.Process != nil {
		endProcess(l.cmd)
		l.cmd = nil
	}
	if l.lease.dir != "" {
		l.cache.release(l.lease)
		l.lease = cacheLease{}
	}
	if l.userDataDir != "" {
		_ = os.RemoveAll(l.userDataDir)
		l.userDataDir = ""
	}
}

// findChrome locates the Chrome binary on the system.
func findChrome() (string, error) {
	if p := os.Getenv("CHROME_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	var candidates []string
	switch runtime.GOOS {
	case "linux":
		candidates = []string{
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
			"/usr/bin/chromium-browser",
			"/usr/bin/chromium",
		}
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		}
	case "windows":
		for _, base := range []string{
			os.Getenv("PROGRAMFILES"),
			os.Getenv("PROGRAMFILES(X86)"),
			os.Getenv("LOCALAPPDATA"),
		} {
			if base != "" {
				candidates = append(candidates, base+`\Google\Chrome\Application\chrome.exe`)
			}
		}
	}

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}

	// Fallback: check PATH.
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium-browser", "chromium", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}

	return "", fmt.Errorf("chrome not found; set CHROME_PATH or install Chrome (tried: %s)",
		strings.Join(candidates, ", "))
}

// uaCache is the on-disk format for the cached UA detection result. An entry
// with no metadata comes from an older format.
type uaCache struct {
	ChromePath    string             `json:"chrome_path"`
	ChromeVersion string             `json:"chrome_version"`
	UserAgent     string             `json:"user_agent"`
	Metadata      *UserAgentMetadata `json:"metadata,omitempty"`
}

// devToolsURLRe matches the line where Chrome prints its DevTools WebSocket URL.
var devToolsURLRe = regexp.MustCompile(`DevTools listening on (ws://\S+)`)

// chromeVersionRe matches one four-part dotted version that is a whole token.
var chromeVersionRe = regexp.MustCompile(`(?:^|\s)(\d+\.\d+\.\d+\.\d+)(?:\s|$)`)

// chromeVersion returns the bare dotted version of the Chrome binary. Windows
// reads the version resource, because a started Chrome passes its arguments to a
// running instance and prints no version.
func chromeVersion(ctx context.Context, chromePath string) (string, error) {
	raw, err := readChromeVersion(ctx, chromePath)
	if err != nil {
		return "", err
	}
	return parseChromeVersion(raw)
}

// parseChromeVersion extracts the four-part dotted version from raw.
func parseChromeVersion(raw string) (string, error) {
	m := chromeVersionRe.FindStringSubmatch(raw)
	if m == nil {
		return "", fmt.Errorf("no four-part version in %q", raw)
	}
	return m[1], nil
}

// uaCachePath returns the path to the UA cache file.
func uaCachePath() (string, error) {
	return appCacheDir("ua_cache.json")
}

// loadUACache reads and validates the UA cache against the current Chrome binary.
// An entry with no metadata is a miss, so the caller detects again.
// Limitation: a Windows feature update changes the platform version, and the cache keeps the old one until the Chrome version changes.
func loadUACache(chromePath, version string) (*UserAgentOverride, bool) {
	cachePath, err := uaCachePath()
	if err != nil {
		return nil, false
	}
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, false
	}
	var cache uaCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, false
	}
	if cache.ChromePath != chromePath || cache.ChromeVersion != version || cache.UserAgent == "" {
		return nil, false
	}
	if cache.Metadata == nil || len(cache.Metadata.Brands) == 0 {
		return nil, false
	}
	return &UserAgentOverride{UserAgent: cache.UserAgent, Metadata: *cache.Metadata}, true
}

// saveUACache persists the UA detection result to disk.
func saveUACache(chromePath, version string, override *UserAgentOverride) error {
	cachePath, err := uaCachePath()
	if err != nil {
		return err
	}
	cache := uaCache{
		ChromePath:    chromePath,
		ChromeVersion: version,
		UserAgent:     override.UserAgent,
		Metadata:      &override.Metadata,
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return fmt.Errorf("marshalling UA cache: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err != nil {
		return fmt.Errorf("creating UA cache directory: %w", err)
	}
	//nolint:gosec // G306: the file holds the Chrome path, its version, the user agent and the UA metadata. None is a secret.
	if err := os.WriteFile(cachePath, data, 0644); err != nil {
		return fmt.Errorf("writing UA cache to %s: %w", cachePath, err)
	}
	return nil
}

// ChromeBinary is the installed Chrome binary.
type ChromeBinary struct {
	// Path is the path of the binary.
	Path string
	// Version is the four-part dotted version of the binary.
	Version string
}

// InstalledChrome finds the Chrome binary and reads its version.
func InstalledChrome(ctx context.Context) (ChromeBinary, error) {
	chromePath, err := findChrome()
	if err != nil {
		return ChromeBinary{}, err
	}
	version, err := chromeVersion(ctx, chromePath)
	if err != nil {
		return ChromeBinary{}, fmt.Errorf("getting chrome version: %w", err)
	}
	return ChromeBinary{Path: chromePath, Version: version}, nil
}

// DetectUserAgent returns the UA string and the UA metadata of bin, with the
// UA of headful Chrome. A disk cache keyed by the binary's path and version
// lets repeat runs skip the detection launch.
func DetectUserAgent(ctx context.Context, logger *slog.Logger, bin ChromeBinary) (*UserAgentOverride, error) {
	logger = logger.With("phase", "ua-detect")

	// Check cache first.
	if override, ok := loadUACache(bin.Path, bin.Version); ok {
		logger.Debug("using cached user-agent", "ua", override.UserAgent)
		return override, nil
	}

	logger.Debug("detecting Chrome user-agent (headless launch)", "version", bin.Version)
	override, err := detectInChrome(ctx, logger)
	if err != nil {
		return nil, err
	}
	logger.Debug("detected user-agent", "ua", override.UserAgent)

	// Save to cache (best-effort).
	if err := saveUACache(bin.Path, bin.Version, override); err != nil {
		logger.Debug("failed to cache user-agent", "error", err)
	}

	return override, nil
}

// detectInChrome reads the UA and the UA metadata in a headless Chrome. The
// chrome://version page is a secure context with no network, so it has
// navigator.userAgentData.
func detectInChrome(ctx context.Context, logger *slog.Logger) (*UserAgentOverride, error) { // coverage-ignore: needs a live Chrome
	l := NewLauncher(logger, nil)
	defer l.Kill()
	// Limitation: the probe runs with no proxy, so its traffic goes direct and unfiltered.
	if err := l.Launch(ctx, true, ""); err != nil {
		return nil, fmt.Errorf("launching chrome for UA detection: %w", err)
	}

	client, err := NewClient(ctx, l.WebSocketURL(), logger)
	if err != nil {
		return nil, fmt.Errorf("connecting to CDP for UA detection: %w", err)
	}
	defer func() { _ = client.Close() }()

	if err := client.CDP.Page.Enable(ctx); err != nil {
		return nil, fmt.Errorf("enabling Page for UA detection: %w", err)
	}
	if err := NavigateAndLoad(ctx, client.CDP, "chrome://version", 10*time.Second); err != nil {
		return nil, err
	}

	var reading uaReading
	if err := evalAwaitingJS(ctx, client.CDP, userAgentJS, &reading); err != nil {
		return nil, fmt.Errorf("reading user-agent: %w", err)
	}
	return reading.override()
}
