// Package main runs the account creation workers from the command line.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sync/errgroup"

	"jagex_account_creator/internal/api"
	"jagex_account_creator/internal/browser"
	"jagex_account_creator/internal/config"
	"jagex_account_creator/internal/jagexapi"
	"jagex_account_creator/internal/proxy"
	"jagex_account_creator/internal/store"
	"jagex_account_creator/web"
)

func main() { // coverage-ignore: needs a live Chrome
	opts, err := parseFlags(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		os.Exit(0)
	}
	if err != nil {
		os.Exit(2)
	}

	var level slog.LevelVar
	logger := newLogger(os.Stderr, colorTerminal(os.Stderr), os.Getenv("NO_COLOR") != "", &level)

	cfg, err := loadConfig(logger, opts)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	if cfg.Debug {
		level.Set(slog.LevelDebug)
	}

	fn, err := modeFunc(opts, logger, &level, cfg, web.SPA)
	if err == nil {
		err = runWithSignals(context.Background(), logger, fn)
	}
	if err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

// loadConfig reads the config file of opts and logs the result. Serve mode
// starts with the defaults when the file is missing, and its first save creates the file.
func loadConfig(logger *slog.Logger, opts cliOptions) (*config.Config, error) {
	cfg, err := readConfig(opts)
	missing := errors.Is(err, fs.ErrNotExist)
	if missing && opts.mode == "serve" {
		logger.Info("config file not found, serving the defaults until the first save creates it", "file", opts.configPath)
		return config.Defaults(), nil
	}
	if missing {
		return nil, fmt.Errorf("%w (copy the template config.yaml to %s)", err, opts.configPath)
	}
	if err != nil {
		return nil, err
	}
	logger.Info("config loaded", "file", opts.configPath)
	return cfg, nil
}

// readConfig reads the config file of opts. Serve mode does not validate it, so
// the dashboard can show and fix a config that a run refuses. Serve refuses the
// config flags, because a save would write them into the file.
func readConfig(opts cliOptions) (*config.Config, error) {
	if opts.mode != "serve" {
		return config.Load(opts.configPath, opts.overrides...)
	}
	if len(opts.overrides) > 0 {
		return nil, errors.New("serve mode refuses -headless, -debug and -count: a run of serve takes its config from the dashboard")
	}
	return config.Read(opts.configPath)
}

// modeFunc returns the work of the mode of opts. The challenge and export modes
// use only the soft stage. The log file of a full run writes at level. Serve
// mode sets level at each run start, and reads its dashboard files from dashboard.
func modeFunc(opts cliOptions, logger *slog.Logger, level *slog.LevelVar, cfg *config.Config, dashboard func() (fs.FS, error)) (runFunc, error) {
	switch opts.mode {
	case "full":
		return func(ctx context.Context, stopNow <-chan struct{}) error {
			return runCLI(ctx, stopNow, logger, level, cfg)
		}, nil
	case "challenge":
		return func(ctx context.Context, _ <-chan struct{}) error { return runChallenge(ctx, logger, cfg) }, nil
	case "export":
		return func(ctx context.Context, _ <-chan struct{}) error { return runExport(ctx, logger, cfg, opts.out) }, nil
	case "serve":
		s := &apiServer{
			cfg:     cfg,
			cfgPath: opts.configPath,
			logger:  logger,
			level:   level,
			runFn:   run,
			events:  newHub[api.RunState](runEventBuffer),
			logs:    newHub[logLine](logLineBuffer),
		}
		return func(ctx context.Context, stopNow <-chan struct{}) error {
			return runServe(ctx, stopNow, s, opts.listen, dashboard)
		}, nil
	default:
		return nil, fmt.Errorf("unknown mode %q", opts.mode)
	}
}

// runWithSignals runs fn and calls its two stop stages on the signals of the process.
func runWithSignals(parent context.Context, logger *slog.Logger, fn runFunc) error {
	// One registration for the whole run, so no signal falls back to the default exit before the last write.
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	c := startRun(parent, fn)
	release := forwardSignals(logger, sigs, c)
	defer release()
	return c.Wait()
}

// forwardSignals reads sigs until release. The first SIGINT calls c.Stop, so
// the work in flight finishes. The second SIGINT, or any SIGTERM, calls c.StopNow.
// Limitation: on Windows, two Ctrl+C presses inside one delivery read as one. That fails toward the soft stage.
func forwardSignals(logger *slog.Logger, sigs <-chan os.Signal, c *runControl) (release func()) {
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		stopping := false
		for {
			select {
			case <-quit:
				return
			case sig := <-sigs:
				if sig == os.Interrupt && !stopping {
					stopping = true
					c.Stop()
					logger.Info("stopping: the work in flight finishes. Press Ctrl+C again to stop at once")
					continue
				}
				c.StopNow()
				logger.Info("stopping at once")
				// The read stops here. The runtime drops the later signals once the buffer is full, so they do not end the process.
				return
			}
		}
	}()
	return func() {
		close(quit)
		<-done
	}
}

// stopNowContext returns a context that the end of ctx does not end. A close
// of stopNow or the returned cancel ends it. A nil stopNow never closes.
func stopNowContext(ctx context.Context, stopNow <-chan struct{}) (context.Context, context.CancelFunc) {
	detached, cancel := context.WithCancel(context.WithoutCancel(ctx))
	go func() {
		select {
		case <-stopNow:
			cancel()
		case <-detached.Done():
		}
	}()
	return detached, cancel
}

// runner holds the dependencies that the attempts of one run share.
// Challenge mode fills only cfg, cache and override.
type runner struct {
	cfg       *config.Config
	mailboxes *mailboxPool // the mailbox of each account
	proxyMgr  *proxy.Manager
	history   runHistory
	cache     *browser.CacheManager
	override  *browser.UserAgentOverride // nil in a headful run
	stopNow   <-chan struct{}            // closed by runControl.StopNow
	state     *runState                  // nil in the CLI
}

// runCLI opens the account store of cfg and runs the full mode as one run of
// the run history. The run logs to logger and to its log file at level. It
// closes the store at the end.
func runCLI(ctx context.Context, stopNow <-chan struct{}, logger *slog.Logger, level slog.Leveler, cfg *config.Config) error {
	// The store opens before the mailbox check, so a run that fails that check has its row.
	accounts, err := openStore(ctx, logger, cfg)
	if err != nil {
		return err
	}
	logOpts := runLogOptions{base: logger.Handler(), level: level}
	err = recordRun(ctx, accounts, api.RunSourceCLI, cfg, logOpts, func(ctx context.Context, logger *slog.Logger, h runHistory) error {
		return run(ctx, stopNow, logger, cfg, nil, h)
	})
	return errors.Join(err, accounts.Close())
}

// run creates the accounts of cfg. The workers mark their progress on state,
// which is nil in the CLI. Each attempt saves its account and its row to history.
func run(ctx context.Context, stopNow <-chan struct{}, logger *slog.Logger, cfg *config.Config, state *runState, history runHistory) error { // coverage-ignore: needs a live Chrome
	mailboxes, accountCount, used, err := mailboxPlan(ctx, history.accounts, cfg)
	if err != nil {
		return err
	}
	logger.Info("mailboxes loaded", "count", len(mailboxes.sources), "already_used", used)
	err = mailboxes.checkLogins(ctx, accountCount, func(ctx context.Context, src config.MailboxConfig) error {
		return buildProviderFromSource(ctx, logger, src).CheckLogin(ctx)
	})
	if err != nil {
		return err
	}

	override, err := resolveUserAgent(ctx, logger, cfg)
	if err != nil {
		return err
	}

	// Each Chrome takes a persistent cache dir from this pool, so it reuses cached JS and CSS across runs.
	cache, err := browser.NewCacheManager("", logger)
	if err != nil {
		return fmt.Errorf("initializing browser cache: %w", err)
	}

	unlimited := accountCount < 0
	if unlimited {
		logger.Info("running in unlimited mode, will create accounts until stopped")
	} else {
		logger.Info("target account count", "count", accountCount)
	}

	proxyMgr, err := runProxies(ctx, logger, cfg, history.accounts)
	if err != nil {
		return err
	}

	r := &runner{cfg: cfg, mailboxes: mailboxes, proxyMgr: proxyMgr, history: history, cache: cache, override: override, stopNow: stopNow, state: state}

	g, gCtx := errgroup.WithContext(ctx)

	if unlimited {
		r.startUnlimitedWorkers(gCtx, g, logger, r.createAccount)
	} else {
		r.startFiniteWorkers(gCtx, g, logger, accountCount, r.createAccount)
	}

	return g.Wait()
}

// openStore opens the account store at the database path of cfg. A new
// database imports the accounts.jsonl file of its dir. The set-up that creates
// the proxies table imports the proxies.txt file of its dir.
func openStore(ctx context.Context, logger *slog.Logger, cfg *config.Config) (*store.Store, error) {
	dir := filepath.Dir(cfg.Database)
	return store.Open(ctx, cfg.Database, filepath.Join(dir, "accounts.jsonl"), filepath.Join(dir, "proxies.txt"), logger)
}

// resolveUserAgent warns when the installed Chrome major differs from the
// shapes' major. Then it returns the UA override of a headless run: the UA and
// the UA metadata of a headful Chrome of the installed version. A headful run
// sends Chrome's own values, so it gets nil.
// Limitation: the headless branch has no test, because seeding the UA cache from here would copy its JSON shape out of internal/browser.
func resolveUserAgent(ctx context.Context, logger *slog.Logger, cfg *config.Config) (*browser.UserAgentOverride, error) {
	bin, err := browser.InstalledChrome(ctx)
	if err != nil {
		return nil, err
	}
	warnChromeMajor(logger, bin.Version)
	var override *browser.UserAgentOverride
	if *cfg.Chrome.Headless {
		if override, err = browser.DetectUserAgent(ctx, logger, bin); err != nil {
			return nil, fmt.Errorf("detecting browser user-agent: %w", err)
		}
	}
	return override, nil
}

// warnChromeMajor logs a WARN when the major of version differs from the
// Chrome major that the HTTP request shapes copy.
func warnChromeMajor(logger *slog.Logger, version string) {
	// Limitation: the check runs at start only, so a Chrome update during a run goes unnoticed until the next start.
	if chromeMajorDiffers(version) {
		logger.Warn("installed Chrome major differs from the Chrome that the request shapes copy",
			"chrome_version", version, "shape_major", jagexapi.ShapeChromeMajor)
	}
}

// chromeMajorDiffers reports whether the major of version differs from the
// Chrome major that the HTTP request shapes copy.
func chromeMajorDiffers(version string) bool {
	major, _, _ := strings.Cut(version, ".")
	return major != strconv.Itoa(jagexapi.ShapeChromeMajor)
}
