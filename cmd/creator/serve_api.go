package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"jagex_account_creator/internal/api"
	"jagex_account_creator/internal/browser"
	"jagex_account_creator/internal/config"
	"jagex_account_creator/internal/jagexapi"
	"jagex_account_creator/internal/store"
)

// apiServer implements the API over the config of serve, as read at start or
// last saved. It runs one run at a time.
type apiServer struct {
	cfgMu   sync.Mutex     // guards cfg, and holds a save from its check to the swap
	cfg     *config.Config // from config.Read, config.Defaults or configFromAPI, so its pointer fields are set
	cfgPath string         // the config file that a save writes
	logger  *slog.Logger
	level   *slog.LevelVar // the level of logger, which each run start sets
	runFn   func(ctx context.Context, stopNow <-chan struct{}, logger *slog.Logger, cfg *config.Config, state *runState, history runHistory) error
	store   *store.Store // the account store of serve, which runServe opens before the listen
	spa     fs.FS        // the dashboard files, which runServe reads before it opens the store

	checkLoginFn func(ctx context.Context, cfg *config.Config) error // test override

	events *hub[api.RunState] // each state of each run, for the life of serve
	logs   *hub[logLine]      // each line of the log file of each run, for the life of serve

	mu      sync.Mutex
	current *runControl // the current or last run, nil before the first start
	state   *runState   // the state of current
	closed  bool        // serve is stopping, so no run starts
}

// handler returns the routes of the API and the dashboard. A body or a
// parameter that does not parse gets the JSON error of requestError, and a
// failed handler gets the JSON error of responseError.
func (s *apiServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", spaHandler(s.spa))
	// An any-method /api/ pattern conflicts with GET / at registration, so this route takes GET only.
	// Limitation: an unknown POST /api/x gets the 405 of the mux with Allow: GET, HEAD, not the JSON 404.
	mux.HandleFunc("GET /api/", s.unknownEndpoint)
	strict := api.NewStrictHandlerWithOptions(s, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  s.requestError,
		ResponseErrorHandlerFunc: s.responseError,
	})
	return api.HandlerWithOptions(strict, api.StdHTTPServerOptions{BaseRouter: mux, ErrorHandlerFunc: s.requestError})
}

// config returns the config of serve. A save replaces the pointer and never
// changes the config that it points to, so the caller can read it with no lock.
func (s *apiServer) config() *config.Config {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	return s.cfg
}

// GetConfig returns the config of serve. It holds no IMAP password.
func (s *apiServer) GetConfig(context.Context, api.GetConfigRequestObject) (api.GetConfigResponseObject, error) {
	return api.GetConfig200JSONResponse(apiConfig(s.config())), nil
}

// SaveConfig validates the posted config, writes it to the config file and
// makes it the config of serve. A run keeps the config that it started with.
// Limitation: a save writes every key, also a key that the file left to its default. It overwrites a hand edit of the file made after serve read it. This fails toward the values of the dashboard.
// Limitation: a -config that names a tracked file, such as the template config.yaml, gets the IMAP password on a save.
func (s *apiServer) SaveConfig(_ context.Context, r api.SaveConfigRequestObject) (api.SaveConfigResponseObject, error) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	cfg, result, err := validatePosted(*r.Body, s.cfg)
	if err != nil {
		return api.SaveConfig400JSONResponse{Message: err.Error()}, nil
	}
	if !result.Valid {
		return api.SaveConfig422JSONResponse(result), nil
	}
	if err := config.Save(s.cfgPath, cfg); err != nil {
		return nil, err
	}
	s.cfg = cfg
	s.logger.Info("config saved", "file", s.cfgPath)
	return api.SaveConfig200JSONResponse(apiConfig(cfg)), nil
}

// ValidateConfig validates the posted config as it is, with no default.
func (s *apiServer) ValidateConfig(_ context.Context, r api.ValidateConfigRequestObject) (api.ValidateConfigResponseObject, error) {
	return validateResponse(*r.Body, s.config()), nil
}

// validateResponse returns the answer to a post of c to a serve that read stored.
func validateResponse(c api.Config, stored *config.Config) api.ValidateConfigResponseObject {
	_, result, err := validatePosted(c, stored)
	if err != nil {
		return api.ValidateConfig400JSONResponse{Message: err.Error()}
	}
	return api.ValidateConfig200JSONResponse(result)
}

// validatePosted returns the config form of c and its validation result, for a
// serve that read stored. An absent imap_pass takes the password of stored.
// configFromAPI fills no default. The database must be the database of stored,
// because serve shows the history of that database only. The error names a
// value that does not parse.
// Limitation: the check compares the paths after filepath.Clean, so two spellings of one file get a refusal. It fails toward a refusal.
func validatePosted(c api.Config, stored *config.Config) (*config.Config, api.ValidationResult, error) {
	cfg, err := configFromAPI(c, stored.Email.IMAPPass)
	if err != nil {
		return nil, api.ValidationResult{}, err
	}
	var msgs []string
	if err := config.Validate(cfg); err != nil {
		msgs = errorMessages(err)
	}
	// config.Validate already names an empty database.
	if cfg.Database != "" && filepath.Clean(cfg.Database) != filepath.Clean(stored.Database) {
		msgs = append(msgs, fmt.Sprintf("database must be the database of serve, %q, got %q", stored.Database, cfg.Database))
	}
	if len(msgs) > 0 {
		return cfg, api.ValidationResult{Errors: msgs}, nil
	}
	return cfg, api.ValidationResult{Valid: true, Errors: []string{}}, nil
}

// CheckChrome finds Chrome and compares its major with the request shapes.
func (s *apiServer) CheckChrome(ctx context.Context, _ api.CheckChromeRequestObject) (api.CheckChromeResponseObject, error) {
	return api.CheckChrome200JSONResponse(chromeCheck(ctx)), nil
}

// chromeCheck finds Chrome and compares its major with the request shapes.
func chromeCheck(ctx context.Context) api.ChromeCheck {
	result := api.ChromeCheck{ShapeMajor: jagexapi.ShapeChromeMajor}
	bin, err := browser.InstalledChrome(ctx)
	if err != nil {
		result.Error = new(err.Error())
		return result
	}
	result.Ok = true
	result.Version = new(bin.Version)
	result.Mismatch = new(chromeMajorDiffers(bin.Version))
	return result
}

// CheckMailbox logs in to the mailbox of the posted email section.
func (s *apiServer) CheckMailbox(ctx context.Context, r api.CheckMailboxRequestObject) (api.CheckMailboxResponseObject, error) {
	cfg := *s.config()
	cfg.Email = emailFromAPI(*r.Body, cfg.Email.IMAPPass)
	result := api.MailboxCheck{Ok: true}
	if err := s.checkLogin(ctx, &cfg); err != nil {
		result = api.MailboxCheck{Error: new(err.Error())}
	}
	return api.CheckMailbox200JSONResponse(result), nil
}

// checkLogin logs in to the mailbox of cfg, as a run start does.
func (s *apiServer) checkLogin(ctx context.Context, cfg *config.Config) error {
	if s.checkLoginFn != nil {
		return s.checkLoginFn(ctx, cfg)
	}
	return newEmailProvider(cfg, s.logger).CheckLogin(ctx)
}

// apiConfig returns the API form of cfg. In place of the IMAP password, it tells whether cfg holds one.
func apiConfig(cfg *config.Config) api.Config {
	return api.Config{
		Debug:    cfg.Debug,
		Database: cfg.Database,
		Chrome:   api.ChromeConfig{Headless: *cfg.Chrome.Headless, Timeout: cfg.Chrome.Timeout.String()},
		Proxy:    api.ProxyConfig{Direct: cfg.Proxy.Direct},
		Email: api.EmailConfig{
			ImapHost:    cfg.Email.IMAPHost,
			ImapPort:    cfg.Email.IMAPPort,
			ImapUser:    cfg.Email.IMAPUser,
			ImapPassSet: new(cfg.Email.IMAPPass != ""),
			ImapDomain:  cfg.Email.IMAPDomain,
		},
		Account: api.AccountConfig{PasswordLength: cfg.Account.PasswordLength, EnableTotp: *cfg.Account.EnableTOTP},
		Concurrency: api.ConcurrencyConfig{
			MaxWorkers:     cfg.Concurrency.MaxWorkers,
			MaxBrowsers:    cfg.Concurrency.MaxBrowsers,
			AccountCount:   cfg.Concurrency.AccountCount,
			AccountTimeout: cfg.Concurrency.AccountTimeout.String(),
		},
	}
}

// configJSON returns the stored form of cfg: the JSON of apiConfig, so it holds no IMAP password.
func configJSON(cfg *config.Config) json.RawMessage {
	text, _ := json.Marshal(apiConfig(cfg)) //nolint:errchkjson // Unreachable: api.Config holds only bools, ints, strings and pointers to them.
	return text
}

// configFromAPI returns the config form of c. An absent imap_pass takes storedPass.
func configFromAPI(c api.Config, storedPass string) (*config.Config, error) {
	chromeTimeout, err := time.ParseDuration(c.Chrome.Timeout)
	if err != nil {
		return nil, fmt.Errorf("chrome.timeout: %w", err)
	}
	accountTimeout, err := time.ParseDuration(c.Concurrency.AccountTimeout)
	if err != nil {
		return nil, fmt.Errorf("concurrency.account_timeout: %w", err)
	}
	return &config.Config{
		Debug:    c.Debug,
		Database: c.Database,
		Chrome:   config.ChromeConfig{Headless: new(c.Chrome.Headless), Timeout: chromeTimeout},
		Proxy:    config.ProxyConfig{Direct: c.Proxy.Direct},
		Email:    emailFromAPI(c.Email, storedPass),
		Account:  config.AccountConfig{PasswordLength: c.Account.PasswordLength, EnableTOTP: new(c.Account.EnableTotp)},
		Concurrency: config.ConcurrencyConfig{
			MaxWorkers:     c.Concurrency.MaxWorkers,
			MaxBrowsers:    c.Concurrency.MaxBrowsers,
			AccountCount:   c.Concurrency.AccountCount,
			AccountTimeout: accountTimeout,
		},
	}, nil
}

// emailFromAPI returns the config form of e. An absent imap_pass takes storedPass.
func emailFromAPI(e api.EmailConfig, storedPass string) config.EmailConfig {
	pass := storedPass
	if e.ImapPass != nil {
		pass = *e.ImapPass
	}
	return config.EmailConfig{IMAPHost: e.ImapHost, IMAPPort: e.ImapPort, IMAPUser: e.ImapUser, IMAPPass: pass, IMAPDomain: e.ImapDomain}
}

// errorMessages returns one message for each error that err joins.
func errorMessages(err error) []string {
	joined, ok := errors.AsType[interface {
		error
		Unwrap() []error
	}](err)
	if !ok {
		return []string{err.Error()}
	}
	var msgs []string
	for _, e := range joined.Unwrap() {
		msgs = append(msgs, e.Error())
	}
	return msgs
}

// requestError answers a request whose body or parameter the generated decode
// refused, as a JSON error. A body over maxBodyBytes gets 413.
func (s *apiServer) requestError(w http.ResponseWriter, _ *http.Request, err error) {
	status, msg := http.StatusBadRequest, err.Error()
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		status, msg = http.StatusRequestEntityTooLarge, fmt.Sprintf("the body is larger than %d bytes", maxBodyBytes)
	}
	s.writeError(w, status, msg)
}

// responseError answers a request whose handler failed, such as a failed store
// read or config save, with 500 and the JSON error. It logs the error at ERROR.
// Neither error holds a secret, so the body carries its text.
// Limitation: an answer whose write fails after its status line also reaches here, so a client that has gone gets an ERROR line and no 500. It fails toward a louder log.
func (s *apiServer) responseError(w http.ResponseWriter, r *http.Request, err error) {
	s.logger.Error("answering the API request", "path", r.URL.Path, "error", err)
	s.writeError(w, http.StatusInternalServerError, err.Error())
}

// writeError writes the JSON error msg with status.
func (s *apiServer) writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(api.Error{Message: msg}); err != nil {
		s.logger.Debug("writing the API error", "error", err)
	}
}
