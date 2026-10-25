# CLAUDE.md — Jagex Account Creator (Go)

## Project Overview

This is a **Jagex account creator** written in Go. It automates the creation of Jagex accounts by:

1. Launching a headless Chrome instance and controlling it via CDP to solve Cloudflare Turnstile challenges
2. Bridging cookies from the browser session into a TLS-fingerprinted HTTP client
3. Performing the actual account registration flow via HTTP requests with the obtained `cf_clearance` cookie

The project uses two core dependencies:

- **`github.com/mafredri/cdp`** — Low-level, type-safe CDP bindings for browser automation
- **`github.com/sardanioss/httpcloak`** — TLS-fingerprinted HTTP client that mimics real browser fingerprints

## Architecture

```
cmd/
  creator/
    main.go              # Entry point, mode dispatch, the signal forwarding and the set-up of a full run
    runcontrol.go        # The two stop stages of one run (Stop and StopNow) and the wait for its result
    runstate.go          # The live state of a run of serve: one row for each worker, the proxy cooldowns and the counts. Each change publishes the new state to the stream clients
    hub.go               # The fan-out of the run state and the log lines to the stream clients. It keeps the last published value for a new client. A publish never blocks. The hub drops a client with a full buffer
    flags.go             # CLI flags and their config overrides
    workers.go           # Worker pool, Chrome slots and the failure backoff
    attempt.go           # One account creation attempt and its browser half
    browserphase.go      # Chrome launch, CDP set-up, the page load and the clearance wait of both modes
    proxy.go             # The proxy load of a run from the store, and the forward proxies and the proxy cooldown of one attempt
    register.go          # The HTTP registration of one attempt and the login-link fetch
    totp.go              # TOTP (2FA) enrollment after registration
    record.go            # The record line, the byte counts and the save of one attempt to the account store
    history.go           # The run history: the run row before the run and its end after it, and the row of each attempt. Also the log file of the run with its `run started` and `run ended` lines
    runlog.go            # The log file of each run: its writer, which also publishes each line to the log stream, the parser of a slog text line, and the replay of the file
    steps.go             # Step timing of one attempt
    logging.go           # The logger of the run: a short colored view on a terminal, every attribute in a file or a pipe. The log file of a run uses the same text form
    console_unix.go      # Terminal check of stderr on Unix
    console_windows.go   # Terminal check of stderr on Windows. It turns on VT processing, so the console shows ANSI colors
    errors.go            # The marker error type: an error that also matches a sentinel and keeps its text
    challenge.go         # Challenge mode: drives one Chrome through the clearance step for debugging
    export.go            # Export mode: writes the store's accounts as JSONL
    serve.go             # Serve mode: the account store of serve, the local HTTP API server, its request checks and its stop
    serve_api.go         # The routes and the API handlers of serve mode: the config, the validation and the Chrome and mailbox checks. Also the JSON error answers
    serve_run.go         # The run handlers of serve mode: the state, the start and the two stop stages, and the wait for the run when serve stops
    serve_events.go      # The two SSE handlers of serve mode: the run state stream, and the log stream, which replays the log file of the run and resumes after Last-Event-ID
    serve_history.go     # The run history handlers of serve mode: the runs, one run, and the attempts of one run, in pages. Also the download of the log file of one run
    serve_accounts.go    # The account handlers of serve mode: the filtered list in pages, one account with its secrets and its current TOTP code, the confirm of one account, and the export in three formats
    serve_proxies.go     # The proxy handlers of serve mode: the list with the health of each proxy, the import, the export, enable, disable, delete and the test of one proxy
    serve_presets.go     # The preset handlers of serve mode: the list, the save by name and the delete
    serve_spa.go         # The dashboard route of serve mode: the files of the build, index.html for a client route, and the JSON 404 of an unknown API path
internal/
  account/
    types.go             # Account data types
    generator.go         # Username, email local part, password, DOB generation
    totp.go              # TOTP (2FA) code generation (RFC 6238), and the current code with the seconds left in its window
  api/
    openapi.yaml         # The HTTP API contract of serve mode (OpenAPI 3.1)
    oapi-codegen.yaml    # The generator config: a net/http ServeMux, a strict server and the models, no client
    generate.go          # The go:generate line, the one pin of the oapi-codegen version
    api.gen.go           # Generated from openapi.yaml. Do not edit
  browser/
    launcher.go          # Chrome process management (find binary, spawn, cleanup, UA detection)
    client.go            # CDP connection wrapper around mafredri/cdp
    page.go              # Page-level helpers (navigate, wait, user agent)
    element.go           # Element finding, shadow DOM piercing, clicking
    cookies.go           # CDP cookie extraction, clearing and logging
    wait.go              # Waits for a done signal, clicking any Turnstile challenge on the way
    clearance.go         # Clearance gate: waits until the JSD report and the managed challenge both set cf_clearance
    errors.go            # Browser error types (ErrChallengeTimeout, etc.)
    cache.go             # CacheManager: the pool of persistent disk cache dirs. Each live Chrome uses one dir in place
    network.go           # CDP network tracker for the DEBUG traffic breakdown (page, plus the children that TargetAttacher passes to it)
    targets.go           # TargetAttacher: auto-attach of child targets (cross-site iframes, workers). Each child gets the UA override and the network tracker subscription before it resumes
    stream.go            # Event pumps: record the events of one CDP stream, or of a cdp.Sync set in the order that the target sent them
    useragent.go         # UA metadata, the Network.setUserAgentOverride call, and the client hint header values
    filter.go            # URL blocking for bandwidth optimization (images, stylesheets, analytics)
    screenshot.go        # Screenshot capture utility
    proc_unix.go         # Unix process group handling
    proc_windows.go      # Windows process group handling
    version_unix.go      # Chrome version via "--version"
    version_windows.go   # Chrome version from the binary's version resource
    cdptest/
      cdptest.go         # Fake CDP domain clients (Runtime, DOM, Input, Page) and a fake event stream for the tests of the browser and creator packages
  bytesize/
    bytesize.go          # Byte counts in binary units for the log lines, for example 530.5KiB
  config/
    config.go            # Configuration loading (YAML), validation, and the save that keeps the comments of the file
    types.go             # Config struct definitions
  cookies/
    convert.go           # Cookie conversion from CDP network.Cookie to httpcloak client.Cookie
  email/
    provider.go          # Email provider interface
    imap.go              # IMAP email provider implementation (with IDLE support)
    mime.go              # MIME message parsing
    errors.go            # Email error types (ErrEmailTimeout, ErrEmailLogin)
  jagexapi/
    client.go            # httpcloak wrapper with cookie import
    requests.go          # Jagex-specific HTTP requests (registration, login and TOTP)
    shapes.go            # The Chrome request shapes: the header order and the fixed values of each kind of request
    errors.go            # HTTP error types (ErrRegistrationFailed, ErrRateLimited) and the response summary of a status error
    retry.go             # Resends a GET after a transport error or a 5xx, with exponential backoff. Sends a POST once
  logtest/
    logger.go            # Test logger that drops the record time, so a test can compare whole lines
  proxy/
    manager.go           # The parser of the proxy file text, the pick of the least loaded proxy and the proxy cooldown
    types.go             # The Proxy type and its host:port forms
    forward.go           # Local forward proxy for Chrome and the HTTP client: domain allowlist, upstream auth (HTTP CONNECT or SOCKS5 upstream), byte count
    proxytest/
      socks5.go          # Fake SOCKS5 server for the tests of the proxy package
  store/
    store.go             # Open and Close of the SQLite account store: file mode 0600, WAL, one connection for the writes, the migrations under PRAGMA user_version
    accounts.go          # WriteAccount: one INSERT for each account. AccountFilter, and the reads Account and Accounts (newest first, in pages)
    jsonl.go             # ExportJSONL of the accounts that a filter selects, the JSON field walk of the record type, and the one-time import of accounts.jsonl into a new database
    export.go            # ExportCSV and ExportUserPass: the CSV and email:password exports of the accounts that a filter selects
    runs.go              # The run history: the run and attempt writes, the paged reads, and the counts of a run from its attempt rows
    proxies.go           # The proxy list: the reads with the health of each proxy from its attempt rows, the import of the proxy file text, enable, disable and delete. Also the one-time import of proxies.txt
    presets.go           # The run presets: Presets in name order, SavePreset by name and DeletePreset
    migrations/
      0001_accounts.sql  # The STRICT accounts table: one typed column for each record field, and extra for the unknown keys of an imported line
      0002_runs.sql      # The STRICT runs and attempts tables. An attempt row links its run, and its saved account when it has one
      0003_proxies.sql   # The STRICT proxies table: one row for each proxy with its credentials. Also the index of the attempts on their proxy
      0004_presets.sql   # The STRICT presets table: one row for each named run config
  useragent/
    clienthints.go       # Shared ClientHints type (used by browser + jagexapi packages). It maps each hint to its header name
    language.go          # The browser language: Chrome's --accept-lang and the HTTP client's accept-language
  waitcheck/             # Test files only: the check that fails a test that waits on the real clock with no guard
    analyzer_test.go     # Finds the code that runs on a test goroutine outside a synctest bubble. The package doc lists the rules
    rules_test.go        # The rules: each wait with no guard
    waitcheck_test.go    # The fixture tests over testdata/, and the tree test over the module's _test.go files and the `*test` helper packages that they import
    testdata/            # One fixture file for each positive and each negative case. helpertree/ is the fixture module of the helper scan
  waittest/
    waittest.go          # Waits for a sync.WaitGroup under a 2s guard that fails the test
scripts/
  ci-job.sh              # The steps of each CI job: ci, ci-windows, mutation and vuln. The workflows and local-ci.sh call it
  local-ci.sh            # Runs the four CI jobs on the head of a pull request on this host and posts each result as a commit status
  golangci-lint.sh       # Builds golangci-lint from source at the pin and runs it (CI and `make lint`)
  coverage-gate.sh       # Test run and coverage gate (CI and `make cover`)
  mutation-gate.sh       # Mutation gate (CI and `make mutate`)
  vuln-gate.sh           # Vulnerability gate (CI and `make vuln`)
  deadcode-gate.sh       # Reachability gate (CI and `make deadcode`)
  web-coverage-gate.sh   # Web coverage gate (CI): the uncovered statements of web/, head against base
  file-state.sh          # The file list of the tree checks of both coverage gates, sourced by each gate
web/                     # The dashboard: a SvelteKit SPA. Serve mode serves its build
  embed.go               # The embed of the dashboard build (all:dist). SPA returns the dist/spa sub-tree, or an error when it has no index.html
  dist/.gitkeep          # Git tracks it, so a clean clone has a dir to embed. The build writes dist/spa
  package.json           # The exact pins, the scripts, the #lib imports and the overrides entry
  package-lock.json      # The locked tree of npm ci
  .npmrc                 # save-exact and engine-strict
  .nvmrc                 # The Node pin of the dashboard
  tsconfig.json          # The 13 strict flags. It extends $app/tsconfig of SvelteKit
  vite.config.ts         # SvelteKit (SPA, dist/spa), Tailwind, the dev proxy to serve, Vitest browser mode with a fixed locale and zone, and coverage
  eslint.config.js       # Type-aware lint and the complexity rules
  .prettierrc.json       # The Prettier options and plugins
  .prettierignore        # The generated types and the tool outputs
  knip.json              # The knip entries
  playwright.config.ts   # e2e: starts $CREATOR_BIN -mode serve on a free port in a temp dir
  stryker.config.json    # Mutation tests on the Vitest runner
  patch-stryker-runner.ts # The postinstall script: two edits of the Stryker Vitest runner. One sets the test name join to ' > '. One fails a run when a test file fails to import
  src/
    app.html             # The page template. Its head script shows a stored light theme before the first paint
    app.css              # The Tailwind entry: the dark color tokens and the spacing tokens under @theme, the light color tokens under data-theme="light", and the term-grid utility
    lib/
      api.ts             # The openapi-fetch client of the serve API
      api.gen.d.ts       # Generated from openapi.yaml by gen:api. Do not edit
      ConfigFields.svelte # The fields that edit a Config, in folding sections, and the mailbox check. They read the edited config from the context of `run/edited.ts`. They know nothing of start or stop
      digest.ts          # The one-line digest of the values of each config section. A closed section of the config form, the run detail and the Checks screen show it
      events.ts          # subscribe: opens one server-sent event stream of JSON values. The Logs screen uses it directly. run/stream.ts uses it for the run state stream
      format.ts          # The one home of the number and time texts: the bytes, the traffic of a phase, the percent, the durations, the relative time and the full time with the zone offset. Also the clock time of a log line, and the tick label of the run chart on a 12-hour clock
      clock.svelte.ts    # clock: the time of a page, with one tick in each period. A page makes one clock, so all its times share one timer
      ui/                # The styled wrappers over native elements, with their component tests. Button has the variants primary, secondary, ghost and danger. ConfirmButton is the two-step confirm of a delete, with text or with icons. Table can pin its actions column to the right edge. Badge shows a label in a tone. Details is the folding section. It keeps its start state, then follows the user. ErrorList is the alert of a list of errors. RelativeTime is the time element of a relative time. Field and Select have the 32 px height of a Button
      shell/             # The parts of the app shell: the nav, the phase badge, the theme toggle and theme.ts, the theme choice and its storage key. With their tests
      run/               # The modules and components of the run screen, with their tests. The screen order is RunControls (start, the two stops and the presets), the status, the chart, then RunForm (the config and its save). edited.ts gives the edited config to them through a context. stream.ts is the one run state stream: the layout opens it, and the run screen, the Proxies screen and the phase badge subscribe. RunChart takes its width from its box, so its labels keep their CSS size
      logs/              # The modules and components of the Logs screen: the feed, the filters and LogList, with their tests. LogList is the virtual list of the lines. It opens at the last line and follows new lines
      runs/              # The module of the Runs screen: the duration, the status badge, the failure text and the config rows of a run, and the optional columns of a page of runs, with its tests
      streamtest/        # Test files only import it: the fake EventSource and the run state of the stream tests
      configtest/        # Test files only import it: the config fixture and the context of the edited config
      clocktest/         # Test files only import it: the host component of the clock test, because a clock starts at component init
      accounts/          # The modules and components of the Accounts screen: the query module, the hidden secret and the live TOTP code, with their tests
      proxies/           # The modules and components of the Proxies screen: the import form, the pool of "Test all enabled", the match of a cooldown to its proxy, the cell of a test (testView) and the optional columns of the table (proxyColumns), with their tests
    routes/              # The root layout with the shell and the run state stream, the run screen at /, the Logs screen at /logs, the Runs screen at /runs and /runs/<id>, the Accounts screen at /accounts and /accounts/<id>, the Proxies screen at /proxies, and the Checks screen at /checks with the Chrome check and the mailbox check, with their component tests
  e2e/                   # The end-to-end tests with axe (the shell, the Checks screen, the run, the config save, the mailbox check, the run presets, the Logs screen, the Runs screen, the Accounts screen and the Proxies screen), the shared helpers (the axe check in both themes, the open of the closed sections, the JSON read, the table rows, the closed port and the listener that never answers), and the account seed accounts.jsonl
.testcoverage.yml        # Config of the coverage tool
.golangci.yml            # Config of golangci-lint
.golangci-lint-version   # Pinned golangci-lint version (CI and `make lint`)
.github/dependabot.yml   # Weekly update pull requests for npm (/web), gomod and github-actions
.github/workflows/       # Both workflows are disabled while GitHub Actions cannot run a job
  ci.yml                 # CI on each pull request and each push to main
  vuln.yml               # Vulnerability gate on each pull request, each day and on demand
Makefile                 # Build automation
go.mod
go.sum
config.yaml              # The config template. Copy it to config.local.yaml
config.local.yaml        # The config of each mode, the default of -config. Git ignores it. Serve saves to it
creator.db               # The account store, the proxy list, the run presets and the run history (SQLite), at the path of the database key
```

### Design Principles

- **No global state.** Pass dependencies explicitly via struct fields or function parameters.
- **Context everywhere.** Every function that does I/O takes a `context.Context` as its first parameter.
- **Errors are values.** Return `error` from every fallible operation. Use `fmt.Errorf("doing X: %w", err)` for wrapping. The function that does the work names it, and a caller wraps only to add a name that the callee's text lacks. Never panic except in truly unrecoverable situations (e.g., programmer error).
- **Structured logging.** Use `log/slog` for all logging. Create child loggers with `.With("component", "browser")` etc.
- **Clean shutdown.** Respect context cancellation. Kill Chrome processes on exit. Close CDP connections. The stop has two stages:
  1. The first Ctrl+C cancels the run context. No new attempt starts. An attempt that has not sent its password stops. The work in flight after the password POST finishes, and the attempt writes its account.
  2. The second Ctrl+C, or a SIGTERM, also stops that work at once and kills Chrome. Such an attempt still writes its account, as unconfirmed.

  `runControl` holds the two stages as `Stop` and `StopNow`. The CLI calls them on the signals. In serve mode, the stop endpoints call them on the run that serve started. The first stage of serve calls `Stop` on that run, and the second stage calls `StopNow`.

---

## Core Dependencies & Import Conventions

### mafredri/cdp

This is the CDP library. It provides type-safe bindings generated from the Chrome DevTools Protocol.

**Import paths:**
```go
import (
    "github.com/mafredri/cdp"
    "github.com/mafredri/cdp/devtool"
    "github.com/mafredri/cdp/rpcc"
    "github.com/mafredri/cdp/protocol/dom"
    "github.com/mafredri/cdp/protocol/input"
    "github.com/mafredri/cdp/protocol/network"
    "github.com/mafredri/cdp/protocol/page"
    "github.com/mafredri/cdp/protocol/runtime"
    "github.com/mafredri/cdp/protocol/target"
    "github.com/mafredri/cdp/session"
)
```

**Key patterns:**

1. **Connection lifecycle:**
```go
devt := devtool.New("http://127.0.0.1:9222")
pt, err := devt.Get(ctx, devtool.Page)
conn, err := rpcc.DialContext(ctx, pt.WebSocketDebuggerURL)
defer conn.Close()
c := cdp.NewClient(conn)
```

2. **Create an event listener before the first event that it must see. A stream holds no event from before its creation:**
```go
// CORRECT: create listener first, then enable
domContent, err := c.Page.DOMContentEventFired(ctx)
defer domContent.Close()
if err = c.Page.Enable(ctx); err != nil { ... }

// Wait for event
_, err = domContent.Recv()
```

3. **Args builder pattern** — most CDP commands use a `NewXxxArgs()` constructor with `.SetYyy()` chainable setters:
```go
navArgs := page.NewNavigateArgs("https://example.com").
    SetReferrer("https://google.com")
nav, err := c.Page.Navigate(ctx, navArgs)
```

4. **Cookie operations:**
```go
// Get all cookies
reply, err := c.Network.GetCookies(ctx, nil)
for _, cookie := range reply.Cookies { ... }

// Clear all cookies
err = c.Network.ClearBrowserCookies(ctx)
```

5. **Runtime.Evaluate for JS execution:**
```go
evalArgs := runtime.NewEvaluateArgs(`document.title`).
    SetReturnByValue(true)
eval, err := c.Runtime.Evaluate(ctx, evalArgs)
// eval.Result.Value is json.RawMessage
```

6. **We ONLY use these CDP domains:** `Page`, `Runtime`, `DOM`, `Input`, `Network`, `Target`, `Browser`. Do NOT use other domains unless explicitly asked.

### sardanioss/httpcloak

This is the TLS-fingerprinted HTTP client. It mimics real browser TLS fingerprints. The client runs in TLS-only mode: httpcloak adds no header. Each request sends only the headers that its shape names, in Chrome's order. The shapes are in `internal/jagexapi/shapes.go`.

**Import paths:**
```go
import (
    "github.com/sardanioss/httpcloak/client"
    "github.com/sardanioss/httpcloak/fingerprint"
)
```

**All HTTP types come from `github.com/sardanioss/httpcloak/client`** — `client.Client`, `client.Request`, `client.Response`, `client.Cookie`. Do NOT use `net/http` types for external requests.

**Key patterns:**

1. **Client creation:**
```go
// presetName follows ShapeChromeMajor, so the TLS fingerprint and the request shapes show one Chrome.
func presetName() string { return "chrome-" + strconv.Itoa(ShapeChromeMajor) }

// NewClient checks the name itself: it stores fingerprint.UnknownPresetError and
// the first Do returns it. So this code needs no nil check.
inner := client.NewClient(presetName(),
    client.WithTimeout(30*time.Second),
    client.WithoutRedirects(),
    client.WithDisableHTTP3(),
    // The preset sets the TLS and HTTP/2 fingerprint and adds no header.
    client.WithTLSOnly(),
)
inner.EnableCookies()
```

2. **Making requests:**
```go
// Build a request struct (not http.NewRequest). In TLS-only mode the request
// sends only its Headers. The Referer field reaches no wire, so the referer is a header.
req := &client.Request{
    Method:  "POST",
    URL:     "https://example.com/api",
    Headers: map[string][]string{
        "accept":     {"application/json"},
        "user-agent": {"Mozilla/5.0 ..."},
        "referer":    {"https://example.com/page"},
    },
    Body:        bytes.NewReader(jsonBody),
    HeaderOrder: []string{"content-length", "accept", "user-agent", "referer", "cookie"},
}
resp, err := inner.Do(ctx, req)
defer resp.Close()
```

3. **Response handling:**
```go
resp.StatusCode              // HTTP status code
resp.Protocol                // e.g. "h2"
body, err := resp.Bytes()    // Read full body
err := resp.JSON(&result)    // Unmarshal JSON body
header := resp.GetHeader("cf-ray") // Get response header
resp.Close()                 // Must be called to release resources
```

4. **Proxy support:**
```go
// The client connects only to its local forward proxy, with no auth.
// The forward proxy holds the upstream URL and its credentials.
inner := client.NewClient(presetName(),
    client.WithDisableHTTP3(),
    client.WithProxy("http://"+fwd.Addr()), // fwd is a *proxy.ForwardProxy
)
```

5. **Cookie jar access:**
```go
jar := inner.Cookies() // Returns cookie jar (nil if EnableCookies not called)
jar.SetCookies(parsedURL, []*client.Cookie{...})
jar.Cookies(parsedURL)     // Returns []*client.Cookie for URL
jar.AllCookies()           // Returns map[domain][]*client.Cookie
```

6. **Header ordering:**
```go
// Each request sets its own HeaderOrder. The client sets no client-wide order.
// The encoder skips a name that the request does not send. cookie (from the jar)
// and content-length (from the body) take the slots that the order gives them.
req.HeaderOrder = []string{"content-length", "sec-ch-ua-full-version-list", ...}
```

---

## Cookie Bridge (CDP → httpcloak)

This is a critical integration point. Chrome solves the Cloudflare challenge. The HTTP client then makes the registration API calls with the cookies from Chrome. The bridge runs two times:

1. Right after the page load, it moves every cookie that Chrome holds at that point, for the early login-link `GET`. These early cookies include `__cf_bm` and `_cfuvid`.
2. At the clearance gate, it moves every cookie. This second import replaces the first by name, domain and path.

The bridge is one-way. No cookie goes from the HTTP client back to the browser.

### CDP `network.Cookie` → `client.Cookie`

The `internal/cookies/convert.go` file handles this conversion:

```go
// CDPToHTTP converts CDP network cookies to httpcloak cookies.
// network.Cookie fields: Name, Value, Domain, Path, Expires, Size, HTTPOnly, Secure, SameSite, etc.
// client.Cookie fields: Name, Value, Domain, Path, Expires (time.Time), HttpOnly, Secure, SameSite (string), etc.
```

**Important details:**
- CDP `Expires` is a `float64` of seconds since the epoch. Convert it with `time.Unix(int64(expires), 0)`.
- CDP `SameSite` is a string enum (`"Strict"`, `"Lax"`, `"None"`). Map to strings `"Strict"`, `"Lax"`, `"None"` (httpcloak uses string values, not typed constants).
- An `Expires` of 0 or less marks a session cookie. Leave the client cookie's `Expires` at its zero value.
- Domain: CDP cookies may have a leading `.` for domain cookies. Preserve it.

---

## Browser Automation Details

### Chrome Launcher

`internal/browser/launcher.go` owns the Chrome process. The functions below hold the paths, the flags and the cleanup steps. Read them for the current values.

1. **Finding the Chrome binary:** `findChrome` checks `CHROME_PATH` first. Then it checks fixed paths for each OS. Then it checks `PATH`.
   At start, `InstalledChrome` finds the binary and reads its version. A WARN tells the operator when its major is not `ShapeChromeMajor`, the Chrome major that the HTTP request shapes copy.
2. **Chrome flags:** `chromeArgs` builds the command line. It sets `--remote-debugging-port=0`, so Chrome picks a free port.
3. **Port discovery:** `awaitDevToolsURL` reads Chrome's stderr for the `DevTools listening on ws://...` line. It stores the WebSocket URL.
4. **Cleanup:** `Shutdown` closes Chrome over CDP with `Browser.close` and waits up to 10 s for the exit. Then it kills Chrome if Chrome did not exit. When its context ends, `Shutdown` kills Chrome at once. `Kill` kills Chrome immediately. Both call `stop`, which releases the cache dir and removes the temp profile dir. Chrome does not trim its HTTP cache, so the release removes a cache dir over `maxCacheBytes`.

### Shadow DOM Piercing

The Turnstile iframe can sit in a shadow root. `querySelector` cannot enter a shadow root. `internal/browser/element.go` finds the checkbox in these steps:

1. `findCheckboxCoordsOnce` takes a `DOM.getDocument` snapshot with depth -1 and `pierce` true. This snapshot reaches the Turnstile iframe.
2. `findIframesBySource` walks the snapshot, shadow roots included. It returns each `challenges.cloudflare.com` iframe in tree order.
3. `DOM.getBoxModel` measures each candidate. `turnstileBoxFitsCheckbox` rejects a box that is too small for the checkbox.
4. `turnstileClickPoint` aims at the checkbox near the left edge of the iframe, with a small random jitter.

The checkbox geometry constants copy the layout of the Cloudflare widget. Update them when Cloudflare changes the widget.

### Element Interaction

`clickAtPoint` (unexported, in `element.go`) sends `mouseMoved`, `mousePressed` and `mouseReleased` through `Input.dispatchMouseEvent`. It adds no delay between the events. `clickTurnstile` finds the checkbox in a new snapshot and calls `clickAtPoint` one time. The jitter on the click point keeps the click off the exact centre of the checkbox. The code uses no `time.Sleep` for the click.

### Waiting Patterns

**Do NOT use `time.Sleep` to wait for page state.** Use these waits:

- `NavigateAndLoad` (`page.go`) creates the `DOMContentEventFired` and `LoadEventFired` listeners before `Page.navigate`. Then it waits for both events.
- `WaitForJS` (`page.go`) polls a JavaScript condition every 200 ms until the condition is true or the timeout ends.
- `ClearanceGate.Wait` (`wait.go`) waits for the done signal of the clearance gate. On each 1 s tick, `clickIfChallenge` calls `clickTurnstile` when the page title marks a challenge.

---

## Account Creation Flow

A run saves its run row to the run history before the first attempt. It saves the end of that row after the last attempt. The run also writes its log file `<database dir>/logs/run-<id>.log`. The first line is `run started`, and the last line is `run ended`.

The overall flow of one attempt is:

```
1. Load proxy from pool: the enabled proxies that the run read from the store at its start. Take the healthy proxy with the fewest attempts in flight. A tie goes to the next proxy in rotation order. The worker waits while every proxy is in cooldown
2. Wait for a free Chrome slot (max_browsers)
3. Start the browser forward proxy. Check the upstream through it (CheckUpstream). A failed check ends the attempt. It puts the proxy in cooldown, unless the stop cut the check short. Then start the HTTP forward proxy
4. Start Chrome with proxy (via the browser forward proxy, which applies the domain allowlist and any upstream auth)
5. Connect via CDP
6. Enable Page and Network domains. In a headless run, set the UA override on the page. Start the CDP network tracker (DEBUG breakdown only), then the target attacher (headless or debug), then the clearance gate. Clear the browser cookies, then set up URL blocking
7. Navigate to the manage page (https://account.jagex.com/en-GB/manage?flow=registration) and wait for the load
8. Read the UA and the nine client hints (the high-entropy six included). Read every cookie that Chrome holds at this point (the early cookies, which include __cf_bm and _cfuvid)
9. Create httpcloak HTTP client with that UA and those hints, through the HTTP forward proxy to the same upstream. The client holds all nine hints. The login-link request shape sends the basic three hints, and the registration shapes send all nine. Import the early cookies
10. Send the login-link GET (/manage/api/oauth/login) in the background
11. Wait for the clearance gate: both Cloudflare passes set cf_clearance (the JavaScript detection report and the managed challenge). On each 1 s tick of the wait, click the Turnstile checkbox if the page is a challenge. The page starts the OAuth redirects in Chrome, and Cloudflare challenges the /oauth2/auth hop. Step 15 walks the redirects over HTTP from the login link
12. Extract ALL cookies from browser via Network.GetCookies
13. Convert cookies to httpcloak client.Cookie format
14. Shut down Chrome in the background while registration runs over HTTP. The Chrome slot frees when Chrome exits
15. Walk the OAuth redirects over HTTP (step oauth_start):
    a. Import every cookie into the httpcloak cookie jar. It replaces the early cookies by name, domain and path
    b. Wait for the login-link GET
    c. Follow the login link to /oauth2/auth. The 302 sets oauth2_authentication_csrf
    d. Follow the login_challenge redirect to registration-start
16. Multi-step registration via httpcloak:
    a. InitRegistration (POST loginChallenge to the registration endpoint)
    b. SubmitInitialRegistration (POST email + DOB), then the verify screen
    c. WaitForVerificationCode (poll email provider)
    d. SubmitVerificationCode (POST code). It polls /current until REGULAR_REGISTRATION_FINALIZING, then enters the account-name screen with one GET
    e. ValidateDisplayName (POST displayName). A name that is not valid fails the attempt. A valid name enters the password screen with one GET
    f. SubmitPassword (POST displayName + password). The state is creating or completed. Any other state fails the attempt. The attempt sends no password when its context has ended. The attempt builds the account record before the POST. A refusal (a 4xx status, a 429 or any other state) writes no account. The error of a refused state carries the reply's `data.reason` as raw JSON
    g. WaitForAccountCreation (poll /current until COMPLETED). The POST and this wait share one 30 s bound. From the POST on, the work ignores the end of the attempt context. Only the second stop signal ends it. Any other failure from the POST on keeps the account, marked unconfirmed

    Every registration request after the init sends `locale: en-GB` and its screen URL as the referer. Every GET of /current sends the ETag of the last full answer in if-none-match, as Chrome does. A 304 keeps the last state. A poll reads at once, then every 2 s
17. If enable_totp: log in over HTTP through registration-continue, then set up TOTP (no second Turnstile solve). 2FA has its own 30 s bound. A refused activation (a 4xx status, a 429 or a bad state) keeps no TOTP secret, and the account stays confirmed. Any other failed activation keeps the TOTP secret and marks the account unconfirmed
18. Save the account to the store (WriteAccount) whenever the attempt sent its password and Jagex did not refuse it, also after a failure. Log one record line for every attempt. Then save the attempt row to the run history (WriteAttempt)
19. Repeat
```

### Configuration

Each mode reads the file of `-config`, by default `config.local.yaml`. Git ignores that file, so the IMAP password stays out of the tree. `config.yaml` is the template: copy it to `config.local.yaml`. The full, challenge and export modes fail when the file is missing. The error names the file and the template.

```yaml
# config.yaml
debug: false             # Enable DEBUG logging and screenshots
database: "creator.db"   # The SQLite account store. Must not be empty

chrome:
  headless: true
  timeout: 30s

proxy:
  # The proxy list is in the account store. With false, a run with no enabled proxy stops at start.
  direct: false          # Run with no proxy and ignore the list

email:
  # Required: imap_host, imap_user, imap_pass, imap_domain.
  # A failed login at start stops the run.
  imap_host: ""
  imap_port: 993
  imap_user: ""
  imap_pass: ""
  imap_domain: ""        # Domain for generated addresses (e.g., "example.com")

account:
  password_length: 16
  enable_totp: true      # Enable 2FA (TOTP) setup after account creation

concurrency:
  max_workers: 10        # Number of parallel account creations
  max_browsers: 0        # Maximum live Chrome processes (0 = max_workers)
  account_count: 10      # Total accounts to create (-1 = unlimited)
  account_timeout: 1m    # Bound on each attempt up to the password POST
```

`account_timeout` covers the attempt up to the password POST. The work after the POST has its own bounds, so the real upper bound is `account_timeout` + 60 s.

The config holds no proxy list. The store holds it, and the Proxies screen edits it. The one-time import of `proxies.txt` from the directory of the database moves a proxy file into the store. See "Account store and exports".

A failed attempt retries its account number after a backoff. Ten failures in a row of one account number stop the run with an error.

### Serve mode

`creator -mode serve` serves the dashboard and its local HTTP API on `-listen`, by default `127.0.0.1:8080`. Serve mode reads the config with the defaults. It refuses the `-headless`, `-debug` and `-count` flags. A run of serve takes its config from the dashboard, and a save would write the flag values into the file. With no config file, serve starts with the defaults and logs one INFO line. The first save creates the file. Serve does not validate the config, so it starts on a config that a run refuses. The one exception is the `database` key: serve opens the store there, and a store that does not open stops serve. A run start validates the posted config and refuses an invalid one with 422.

Serve opens one account store at its own `database` before it listens. So on a new database, the one-time `accounts.jsonl` import runs when serve starts, before the listen. The one-time `proxies.txt` import also runs there. A store that does not open stops serve before it binds the port. Each run that serve starts saves its accounts and its history to that store. Serve closes the store after the run ends.

The dashboard is the build of `web/`, which the binary embeds (see "Front end (web/)"). A start with no built dashboard stops serve before it opens the store. The error names `npm --prefix web run build`. A clean clone builds and tests with no Node, because git tracks `web/dist/.gitkeep`.

The dashboard routes:

- A `GET` of a path that names a file of the build gets the file.
- A `GET` of a missing path under `/_app/` gets 404.
- Any other `GET` path is a client route and gets `index.html`.
- Outside `/_app/`, a path that ends in `/index.html` gets a 301 to `./`.
- A `GET` of an unknown `/api/` path gets the JSON `Error` 404.
- A `GET` of `/api` with no slash gets a 307 to `/api/`, and then the JSON 404.

The dashboard routes pass the same request checks as the API.

Limitation: an unknown `POST /api/x` gets the 405 of the mux with `Allow: GET, HEAD`, not the JSON 404.

Limitation: a link to the dashboard from another site gets 403, because the browser sends `Sec-Fetch-Site: cross-site`.

Limitation: the content type of a file comes from the mime table of Go, and the mime table of the OS overrides it. On Windows, the OS table is the registry.

A posted config whose `database` differs from the database of serve is invalid. `POST /api/config/validate` reports it, and `POST /api/run`, `PUT /api/config` and `POST /api/presets` answer 422. The check compares the two paths after `filepath.Clean`. Limitation: the check also refuses two spellings of one file. This fails toward a refusal.

The contract is `internal/api/openapi.yaml` (OpenAPI 3.1). oapi-codegen generates `internal/api/api.gen.go` from it. The `//go:generate` line in `internal/api/generate.go` pins the generator version. `make generate` runs it, and CI fails when the committed file differs from the generator output. Do not edit `api.gen.go`. openapi-typescript generates the dashboard types `web/src/lib/api.gen.d.ts` from the same spec. `npm --prefix web run gen:api` runs it, and the same CI step checks that file.

The API never sends the IMAP password. `GET /api/config` sends `imap_pass_set` in its place. In a request, an absent `imap_pass` means the password of the config of serve, as read at start or last saved.

Limitation: a preset holds no IMAP password, so a preset of another mailbox needs the password typed before Start. It fails toward a 422 or a run against serve's mailbox, never toward a stored password.

`PUT /api/config` saves the config of serve:

1. It validates the posted config as a run start does. A body that does not parse gets 400. An invalid config gets 422.
2. It writes the config to the file of `-config`. The file keeps its comments and its key order. A file with an unknown key or a wrong type refuses the save, stays unchanged and gets 500. The write goes to a temp file with mode 0600 in the same dir, then one rename replaces the file. A failed read or write also gets 500.
3. The saved config becomes the config of serve, and serve logs one INFO line `config saved` with the path. The answer is 200 with the saved config, with no IMAP password.

A run keeps the config that it started with, so a save during a run does not change the run. One lock holds a save from the validation to the swap, so two saves do not interleave.

Limitation: a save drops the blank lines between sections, the alignment of line comments and the quote style of a value. The comment text stays. A CRLF file comes back with LF line endings, and its first save puts one blank line inside each multi-line comment block.

Limitation: a save writes every key, also a key that the file left to its default. It overwrites a hand edit of the file made after serve read it. This fails toward the values of the dashboard.

Limitation: on Windows, a rename over a file that another process holds open fails, and the old file stays. Mode bits do nothing on Windows. A `-config` symlink becomes a plain file.

Limitation: a `-config` that names a tracked file, such as `config.yaml`, gets the IMAP password on a save.

Every request passes these checks in order:

1. The Host check. The host name must be `localhost` or an IP address, with or without a port. Any other host gets 421. This check stops a DNS rebinding page.
2. A `Sec-Fetch-Site` of `cross-site` or `same-site` gets 403, on every method.
3. `http.CrossOriginProtection`.
4. The body bound of 64 KiB. A larger body gets 413.
5. The `Cross-Origin-Resource-Policy: same-origin` and `X-Content-Type-Options: nosniff` headers.

Serve holds one run at a time. These endpoints control it:

- `GET /api/run` returns the state of the current or last run.
- `GET /api/events` sends the current run state, then the full state after each change, as server-sent events. Each event is the same document as the answer of `GET /api/run`.
- `GET /api/logs` sends the log lines of the current or last run that serve started, as server-sent events. It first sends the lines that the log file of the run holds, then each new line. A new run sends its lines on the same stream. Each event holds one line in its parts as one JSON object. Each event also has an id, `<run_id>:<end>`, where `end` is the byte offset after the line in the file. The lines are at the level of the stderr lines. Serve's own lines go only to stderr.
- `POST /api/run` validates the posted config and starts a run with it. It returns 202 and the state. A run in progress, or a serve that stops, gets 409. An invalid config gets 422. A body that does not parse gets 400.
- `POST /api/run/stop` calls the first stop stage of the run.
- `POST /api/run/stop-now` calls the second stop stage of the run.

Each stop returns the state. With no run in progress, a stop changes nothing.

A stream ends when serve stops, or when the buffer of its client fills. The client then connects again. The run state stream sends the current state first. A log client sends the id of its last event in `Last-Event-ID`, and the log stream sends only the lines after it. So the client loses no line of the run that the stream follows. An id of another run, or an id that does not parse, gets every line of that run. So a client that connects again after the start of a new run does not get the rest of the old run. `GET /api/runs/{id}/log` still sends the old file.

Limitation: after a restart of serve, the log stream sends nothing until the next start. The log file of the last run stays on disk, and `GET /api/runs/{id}/log` still sends it.

Limitation: on a LAN `-listen` address, a debug run streams its DEBUG lines to any client on the LAN. Its log file also goes to any such client. These lines hold secrets, and the API has no login.

Limitation: on a LAN `-listen` address, any client on the LAN can read the passwords, the TOTP secrets and the proxy credentials. The account detail, the account exports and the proxy export send them, and the API has no login.

These endpoints read the run history of the store:

- `GET /api/runs` returns a page of the runs, newest first. Each run holds its config and its counts. The config holds no IMAP password.
- `GET /api/runs/{id}` returns one run. A run that does not exist gets 404.
- `GET /api/runs/{id}/attempts` returns a page of the attempts of one run, newest first. A run that does not exist gets 404.
- `GET /api/runs/{id}/log` returns the log file of one run as `text/plain`. The `Content-Disposition` header marks the file as an attachment named `run-<id>.log`. For a run in progress, the answer holds the bytes that the file held at the request. A run that does not exist, or a run with no log file, gets 404.

These endpoints read the accounts of the store, and the confirm writes one account:

- `GET /api/accounts` returns a page of the accounts that the filters select, newest first. An account of the list holds no password, no TOTP secret and no backup codes. The answer also holds `total`, the count of all the accounts that the filters select.
- `GET /api/accounts/{id}` returns one account with every field, the password, the TOTP secret and the backup codes included. An account with a TOTP secret also gets `totp_code` and `totp_remaining_seconds`. They are the code of the current 30 s window and the seconds left in it, from the server clock. A secret that does not decode gets neither. An account that does not exist gets 404.
- `POST /api/accounts/{id}/confirm` marks one account as checked by hand. It sets `unconfirmed` to false and returns the account with no secrets. A second call changes nothing and also gets 200. An account that does not exist gets 404. Serve logs one INFO line `account confirmed` with the id.
- `GET /api/accounts/export?format=<format>` returns one file with every account that the filters select, in id order, with no paging. The `Content-Disposition` header marks the file as an attachment named `accounts.jsonl`, `accounts.csv` or `accounts.txt`.

Limitation: a failed hand check has no control. The account stays unconfirmed, and an `unconfirmed=false` export leaves it out.

Limitation: a confirm has no undo.

Limitation: a confirm of an account whose 2FA activation was unconfirmed leaves `backup_codes` absent.

Limitation: after a confirm, the unconfirmed count of the live run state and the count of that run in the run history differ.

The list and the export take the same filters. Each filter is optional. An account must match each filter that the request sets:

- `run`: the run of the attempt that saved the account.
- `unconfirmed`: `true` for the unconfirmed accounts, `false` for the others.
- `totp`: `true` for the accounts with a TOTP secret, `false` for the accounts without one.
- `proxy`: the proxy of the account. The match is exact.
- `created_from`: the earliest creation time. The bound is inclusive.
- `created_to`: the end of the creation times. The bound is exclusive.
- `q`: a text in the email or the username. The search ignores the case of ASCII letters.

The export formats are the formats of the store exports (see "Account store and exports"):

- `jsonl` (`application/x-ndjson`): the bytes of `ExportJSONL`. With no filter, they are the bytes of the CLI export.
- `csv` (`text/csv`): the bytes of `ExportCSV`.
- `userpass` (`text/plain`): the bytes of `ExportUserPass`.

The export builds the whole file before it sends the status. So a failed store read gets a JSON 500, never a part of a file.

A page holds at most `limit` items, from 1 to 500. The default is 100. A page holds only the items with an id below `before`. An absent `before` gives the first page. The answer holds `next_before`, the `before` of the next page. On the last page, `next_before` is absent. The `total` of the accounts list does not change with `before` and `limit`, so each page of one filter has the same `total`.

A `before` or a `run` below 1, a `limit` out of range, or an id or a filter that does not parse gets 400. An absent `format`, or a `format` that is not one of the three, also gets 400. A failed store read or write gets 500 and one ERROR line with the path. Each error answer is a JSON `Error`.

These endpoints read and write the proxy list of the store (see "Proxy Support"):

- `GET /api/proxies` returns every proxy with its health, in id order, with no paging. An item holds the `HostPort` form of the proxy and `auth`, which is true when the proxy has credentials. It holds no user name and no password. The health is the attempts, the successes, the four byte totals, `last_attempt_at` and `last_failure`.
- `POST /api/proxies/import` saves each proxy of a proxy file text (`text/plain`). It returns the counts `added`, `updated` and `repeated`. A bad line gets 400, and no proxy changes. The message names only the line number. Serve logs one INFO line `proxies imported` with the counts.
- `GET /api/proxies/export` returns one proxy file with every proxy, in id order, with its credentials. The `Content-Disposition` header marks the file as an attachment named `proxies.txt`. An import of the file gives the same proxies. The file holds no enabled state.
- `POST /api/proxies/{id}/enable` and `POST /api/proxies/{id}/disable` set the enabled state of one proxy and return the proxy. A second call changes nothing and also gets 200. Serve logs one INFO line `proxy enabled` or `proxy disabled` with the id.
- `DELETE /api/proxies/{id}` deletes one proxy and returns 204. Its attempt rows stay, so a later import of the same proxy shows its health again. Serve logs one INFO line `proxy deleted` with the id.
- `POST /api/proxies/{id}/test` checks that the upstream of one proxy accepts a connection. It returns `ok`, `duration_ms` and, on a failure, `error`.

A proxy that does not exist gets 404.

These endpoints read and write the run presets of the store (see "Account store and exports"):

- `GET /api/presets` returns every preset, in name order, with no paging. A preset holds its id, its name, its config and the time of its last save.
- `POST /api/presets` saves the posted config under the posted name. It validates the config as a run start does. A preset with the same name gets the new config and keeps its id. The answer is 200 with the preset. Serve logs one INFO line `preset saved` with the id and the name.
- `DELETE /api/presets/{id}` deletes one preset and returns 204. Serve logs one INFO line `preset deleted` with the id.

The list and the save answer each preset with the values of the config of serve in `database` and `email.imap_pass_set`. They do not send the stored values.

An empty name, a body that does not parse, or a bad duration gets 400. An invalid config, or a `database` other than the database of serve, gets 422. A preset that does not exist gets 404.

The run state holds:

- The phase: `idle`, `running`, `stopping`, `stopping_now` or `ended`.
- `run_id`, the id of the run row in the run history. It is absent before the first run. It is also absent in the start answer until the run row exists.
- One row for each worker, for the whole run. A row holds a wait for a proxy or a backoff, or an attempt with its open step. A row with neither is idle. A worker that waits for a Chrome slot has an attempt with the open step `browser_slot_wait`.
- The proxy cooldowns that still run, each with its reason and its end.
- The counts: the attempts, the accounts, the accounts that were unconfirmed at their save, the failures by category and the four byte totals.

Each start sets the log level from the posted `debug`. The log file and the log stream of the run use that level. Limitation: serve's own lines stay at that level until the next start.

On a stop, serve does these steps:

1. It refuses new starts and calls `Stop` on the run.
2. It keeps serving, so a client sees the stop, and waits for the run to end. A second stop signal calls `StopNow` on the run.
3. It closes the run state stream and the log stream.
4. It waits up to 5 s for the open requests. A second stop signal ends the wait at once.
5. It closes the connections that are still open.

A serve error also stops the run and waits for it.

### Account Data Generation

```go
// Use crypto/rand for password generation, NOT math/rand
// Usernames: random lowercase alphanumeric, 12 chars
// Passwords: crypto/rand, mix of upper/lower/digit/special, configurable length
// DOB: random date making the account 18-30 years old
```

### Account store and exports

The account store (`internal/store`) is a SQLite database at the path of the `database` key. Each account is one row of the `accounts` table. The table has one typed column for each record field. Its `extra` column holds the keys of an imported line that the record type does not know.

`Open` creates the database file with mode 0600 before SQLite opens it. It sets WAL, a busy timeout and foreign keys. One connection serializes the writes of the workers. The embedded files in `internal/store/migrations` run in order under `PRAGMA user_version`, in one transaction. Each file sets the version that its filename names.

`WriteAccount` saves one account with one INSERT. Each call returns its own error, and a failed call does not stop later calls. The `totp_secret` and `backup_codes` fields are present when `enable_totp` is true and 2FA enrollment succeeded. When enrollment fails before the activation, or Jagex refuses the activation, the account is still saved without them and a WARN names the cause. When the activation fails in any other way, the account keeps `totp_secret` without `backup_codes`.

The store holds every account that the attempt sent the password of, also when the attempt failed after the POST. It holds no account whose password POST Jagex refused. A refusal is a 4xx status, a 429, or a reply whose state is not creating or completed. Limitation: no run checked an account after a 4xx or a 429, so this rule fails toward a lost account. The store marks an account unconfirmed (`"unconfirmed":true` in the export) when the process did not see Jagex confirm its last change. With no `totp_secret`, that change is the creation. With a `totp_secret` and no `backup_codes`, it is the 2FA activation. The operator checks an unconfirmed account by hand. Then the confirm (`POST /api/accounts/{id}/confirm`) sets its `unconfirmed` to false, and the export shows it as an account that the run confirmed.

The store also holds the run history in two tables:

- `runs`: one row for each run of the full mode or of serve. The row holds the source (`cli` or `serve`), the start, the end, the error and the config of the run. The config holds no IMAP password.
- `attempts`: one row for each attempt that ended. The row holds the fields of the record line, except `unconfirmed`, and the worker number. It links its run. It also links its saved account, when the attempt saved one.

The counts of a run come from its attempt rows. The unconfirmed count reads `unconfirmed` of the linked accounts, so a confirm lowers it. The live run state of serve keeps its count at save. The run row holds no total. Challenge mode saves no history. It opens the store only to read the proxies, then closes it.

The store also holds the proxy list in the `proxies` table. A row holds the `HostPort` form of one proxy, its credentials, its enabled state and the time of its import. The `HostPort` form is unique. `attempts.proxy` holds the same form.

The health of a proxy comes from the attempt rows whose `proxy` is its `HostPort` form. One query groups these rows by proxy, through the index on `attempts (proxy, id)`:

- The attempts and the successes. A success is an attempt with no failure category.
- The four byte totals.
- The start of the newest attempt, and the category of the newest failed attempt.

A delete of a proxy keeps its attempt rows.

Limitation: an attempt that failed outside the proxy, for example on the mailbox, counts against its proxy. This fails toward a lower success count.

The store also holds the run presets in the `presets` table. A row holds one named config: the name, the config as a JSON object and the time of its last save. The name is unique and not empty. The config has the form of the config of a run row, with no IMAP password. `configJSON` in `cmd/creator/serve_api.go` is the one home of that form.

`Presets` returns every preset in name order. The order ignores the case of ASCII letters, and the id orders two names that differ only in case. `SavePreset` saves a config by name. A preset with the same name gets the new config and keeps its id. `DeletePreset` deletes one preset. Its error matches `ErrNotFound` when no preset has that id.

Limitation: the unique check of the name is case-sensitive, so `Fast` and `fast` are two presets. This fails toward one more preset, never toward a lost one.

`recordRun` saves the run row before the run and the end of the row after the run. Neither save uses the run context, so a stop does not end them. A failed start save fails the run before any attempt. A failed end save joins the run error.

`recordRun` also writes the log file of the run. The full mode and serve both write it. Challenge mode writes none.

1. After the start save, it creates `logs/` in the directory of the database with mode 0700. It creates `run-<id>.log` there with mode 0600 and `O_EXCL`. A failed open is the run error, and the run does not start.
2. The run logger sends each line to stderr and to the file. The file holds the slog text with every attribute, as a stderr that goes to a file gets it. The file gets the lines at the level of the run.
3. The first line is `run started`. The last line is `run ended`, with the run error when the run has one.
4. After the end save, it closes the file. A failed close joins the run error.

In serve, each line that the file gets also goes to the log stream.

Limitation: the log file of a debug run holds the secrets of its DEBUG lines. The file has mode 0600, as the store has. Mode bits do nothing on Windows.

Limitation: a failed append to the log file is silent, because slog drops the error of a handler. stderr keeps the line. The log stream does not get it.

Limitation: no run removes an old log file. The operator removes the old files in `logs/`.

Limitation: `O_EXCL` refuses a file that exists. So a `logs/` dir kept from a removed database refuses the start of a run whose id it already holds. The error names the file.

`finish` saves the attempt row (`WriteAttempt`) after the record line, also after the end of the attempt context. A failed attempt save logs one WARN and changes nothing else. The worker decides its retry from the attempt error, so that error must not change.

Limitation: a `save_failed` attempt has no saved account, so its row has no email and no username. The record line keeps them.

Limitation: a process that dies leaves its run row with no end. The runs list shows such a run as unfinished.

Limitation: a read holds the one connection, so the save of a worker waits for the end of the read. This fails toward a late save, never a lost one. An export reads all its rows before it writes, so a slow client does not hold the connection.

The full mode opens the store before the mailbox check, so the two imports below run before that check. On a new database only, the run imports `accounts.jsonl` from the directory of the database, one time:

1. The import runs in the same transaction as the migrations.
2. It reads each row back on that transaction and compares it with its input line.
3. A bad line, a repeated email or a mismatch fails the start. The error names only the line number.
4. A failed start leaves no rows, so the next start tries the import again.
5. One INFO line `imported accounts` gives the count and the path.

The import never writes the source file. After the import, `accounts.jsonl` is a stale copy, and the store is the one record. The run never imports a file beside an existing database.

The set-up that creates the `proxies` table imports `proxies.txt` from the directory of the database, one time. This set-up runs on a new database, and on the first open of a database that has no `proxies` table:

1. The import runs in the same transaction as the migrations. It follows the import rules of "Proxy Support".
2. A missing file imports nothing.
3. A bad line fails the start. The error names only the line number. A failed start leaves no rows and no `proxies` table, so the next start tries the import again.
4. When the import adds a proxy, one INFO line `imported proxies` gives the count of the added proxies and the path.

The import never writes `proxies.txt`. After the import, the file is a stale copy, and the store is the one proxy list.

`creator -mode export -out <path>` writes every account as JSONL (`ExportJSONL` with an empty `AccountFilter`), one JSON object per line, in id order. The keys of an imported line that the record type does not know follow the known fields. The export creates the file with mode 0600. It fails when the file exists, and it removes the file on an error. Export mode needs a full valid config, as the full and challenge modes do. A line of the export:

```json
{"email":"user@example.com","username":"randomuser123","password":"SecureP@ss!23","totp_secret":"BASE32SECRET","backup_codes":["CODE1","CODE2"],"created_at":"2025-01-15T10:30:00Z","proxy":"host:port","browser_bytes_sent":12345,"browser_bytes_received":543210,"http_bytes_sent":23456,"http_bytes_received":65432}
```

`AccountFilter` selects the accounts of a read or an export. Its zero value selects every account. `Accounts` returns one page of the accounts that a filter selects, newest first. `Account` returns one account by its id. Its error matches `ErrNotFound` when no account has that id. `ConfirmAccount` sets `unconfirmed` of one account to false with one UPDATE. Its error also matches `ErrNotFound` when no account has that id.

Each export writes the accounts that a filter selects, in id order:

- `ExportJSONL` writes the JSONL lines above. With an empty filter, it writes the bytes of the CLI export.
- `ExportCSV` writes a header row, then one row for each account. The header holds the JSON names of the `account.Account` fields, in the order of the record. Each row holds every column. A bool cell is `true` or `false`. An int64 cell is its decimal value, `0` included. A text, list or time cell is empty when the JSONL line omits the key. One space joins the backup codes. The file holds only the known fields. The keys of an imported line that the record type does not know stay in the JSONL export.
- `ExportUserPass` writes one `email:password` line for each account. Jagex logs in with the email. The username is the display name.

`jsonFields` is the one walk of the record fields. The import reads the known keys from it, and the CSV export reads its columns from it.

Limitation: a generated password can start with `@`, and a spreadsheet reads such a CSV cell as a formula. The cell keeps the real value. So the file fails toward a cell that a spreadsheet misreads, never toward a changed password.

The four byte fields count the bytes that the forward proxy of each phase sends to and receives from the upstream. The `browser_` fields count the Chrome phase. The `http_` fields count the HTTP phase. The count includes the CONNECT request and its auth, TLS, headers and bodies. It does not include the SOCKS5 handshake. The fields are absent on a run with no proxy.

Each attempt logs one record line to stderr and to the log file of the run: `account created` (INFO) or `account creation failed` (ERROR). The line carries the proxy, the four byte counts, the total duration and a `steps` group with the duration of each step. A failed line also carries the category, the error, `failed_step` and `failed_phase`. The line of an unconfirmed account, failed or not, carries `unconfirmed=true` beside the `email` and the `username`. The attempt row of the run history holds the same fields, except `unconfirmed`. It reads the email and the username from its saved account.

A line that goes to a file or a pipe keeps every attribute in the `slog` text format. On a terminal, the line has no `steps` group. It shows the byte counts in binary units, for example `530.5KiB`, and the error in red. A set `NO_COLOR` keeps this view without color. The log file of the run keeps the step durations.

A failed line keeps the category of its cause. The work after the password POST ignores the attempt context, so a failure from its own bound has the category `unknown`.

A failed save has the category `save_failed`. Its line carries the `email` and the `username` of the account. After a success, it has no `failed_step` and no `failed_phase`, because the save runs after the step clock stops. A failed save of an unconfirmed account keeps both errors and the failed step of the cause.

The first step, `browser_slot_wait`, is the time that the attempt waits for a free Chrome slot. It is part of `duration`. `account_timeout` does not cover it. The wait for a proxy comes before this step. It is not part of `duration`.

---

## Proxy Support

The browser phase and the HTTP phase use the same upstream proxy. Each phase connects through its own local forward proxy (`internal/proxy/forward.go`), so each phase has its own byte count:

1. **Chrome:** Chrome connects to its forward proxy through `--proxy-server`, with no auth.
2. **httpcloak:** httpcloak connects to its forward proxy through `client.WithProxy("http://"+fwd.Addr())`, with no auth. httpcloak never sees the upstream URL.

Each forward proxy connects to the upstream, with credentials when the proxy has them. It speaks HTTP CONNECT or SOCKS5 to the upstream. A run with no proxy has no forward proxy and no byte count.

The `proxies` table of the store is the one proxy list. `runProxies` (`cmd/creator/proxy.go`) loads the proxies of one run:

1. With `proxy.direct` set, the run connects with no proxy and ignores the list. One WARN says so.
2. Else the run reads the enabled proxies, in id order. With no enabled proxy, the run stops at start, so a run never connects from the host IP by accident.
3. One INFO line `proxies loaded` gives the count.

The full mode and serve load the proxies after the mailbox check and the Chrome detection. Challenge mode loads them in the same way. A run keeps the list that it read at its start. An import, an enable, a disable or a delete during a run applies at the next start.

The proxy file text is the format of the import and of the export. Each line is `[scheme://]host:port[:user:pass]`. The scheme is `http`, `socks5` or `socks5h`, and the parser reads `socks5h` as `socks5`. A line with no scheme uses `http`. The parser skips an empty line and a line that starts with `#`. `proxy.Parse` is the one parser. An import follows these rules:

- A bad line fails the whole import, and no proxy changes. The error names only the line number, never the line text.
- Two lines with one `HostPort` form are one proxy. The proxy gets the credentials of the last line. Each earlier line counts as repeated.
- The import enables a new proxy.
- A proxy that the store holds with other credentials gets the new credentials and counts as updated. Its enabled state does not change.
- A proxy that the store holds with the same credentials changes nothing and counts as repeated.

`Proxy.Line` writes the line of one proxy with its credentials, so an import of the export gives the same proxies.

A 429 response puts the proxy in cooldown, as a failed connectivity check does. A failure during a cooldown that still runs does not lengthen it. A check that the stop cuts short cools nothing. When every proxy is in cooldown, `Next` waits until the first cooldown ends. The cooldowns live only in the run. The run state carries each cooldown that still runs.

The test endpoint (`POST /api/proxies/{id}/test`) runs the connectivity check of an attempt on one proxy. It starts a forward proxy for the proxy and calls `CheckUpstream`, which opens a TCP connection to the upstream within 2 s. The test changes no cooldown, because no run reads its result.

Limitation: the test sends no request through the proxy, so it does not check the credentials. A proxy with bad credentials passes. This fails toward a pass.

The forward proxy (`proxy.ForwardProxy`) restricts connections to an allowlist of domains (jagex.com, cloudflare.com, ipify.org). For an HTTP upstream, it also keeps an idle connection pool. A SOCKS5 connection is bound to one target, so a SOCKS5 upstream has no pool. For an HTTP upstream, the connectivity check (`CheckUpstream`) dials through the pool of the browser forward proxy, and the first CONNECT reuses that connection. `Bytes` returns the bytes that the forward proxy sent to and received from the upstream. At Stop, each forward proxy logs one DEBUG line per CONNECT host with the connect count and the bytes each way. Each upstream failure logs one DEBUG line `upstream failed` with the host, the status that the client gets, and the error.

---

## Front end (web/)

`web/` holds the dashboard. It is a SvelteKit SPA, and serve mode serves its build. The pages call the API of serve through `openapi-fetch`. The root layout opens the one run state stream through `EventSource`. The run screen, the Proxies screen and the phase badge share that stream. The Logs screen reads the log stream through its own `EventSource`. `openapi-typescript` generates the types of the API from `internal/api/openapi.yaml`.

### Stack

`web/package.json` holds an exact pin for each dependency. Git tracks `web/package-lock.json`.

- Node: `web/.nvmrc` holds the one exact pin, 26.12.1. The `engines.node` field of `web/package.json` is `26.x`.
- `web/.npmrc` sets `save-exact` and `engine-strict`. So `npm install <package>` writes an exact pin, and a Node outside 26.x refuses `npm ci`.
- Svelte 5 and SvelteKit 3 with adapter-static. The build is an SPA with an `index.html` fallback, in `web/dist/spa`.
- Vite and Tailwind 4.
- TypeScript 6.0.3. `web/tsconfig.json` sets 13 strict flags.
- ESLint with typescript-eslint, eslint-plugin-svelte and eslint-plugin-sonarjs.
- Prettier with prettier-plugin-svelte and prettier-plugin-tailwindcss.
- svelte-check and knip.
- Vitest 5.0.5 in browser mode, with Playwright Chromium, vitest-browser-svelte and v8 coverage.
- Playwright with @axe-core/playwright for the end-to-end tests.
- StrykerJS with its Vitest runner for the mutation tests.
- `d3-scale` for the scales and the ticks of the run chart, and `d3-time` for the round time steps of those ticks.
- `@tanstack/svelte-virtual` for the virtual list of the Logs screen.
- `@lucide/svelte` for the icons.

SvelteKit 3 refuses a `svelte.config.js`, so `web/vite.config.ts` holds the kit options. SvelteKit 3 has no `$lib` alias. Import `#lib/<file>` with its extension, for example `#lib/api.ts`. The `imports` field of `web/package.json` maps it.

A control is a native element, such as `<button>` or `<input>`. `web/src/lib/ui/` holds the styled wrappers over these elements. A screen adds a wrapper only for an element that no screen styles yet. Each `Button` sets its variant: `primary`, `secondary`, `ghost` or `danger`. `Badge` shows a short label in one tone. `ErrorList` is the one alert of a list of errors.

`ConfirmButton` is the one two-step confirm of a delete:

- Its `size` is `default`, with text, or `icon`, with a trash icon.
- A click on Delete starts the confirm. The same button becomes Cancel, so the focus stays on it. "Confirm delete" shows after it.
- The confirm ignores a click with a `detail` above 1. So the second click of a double click does not delete. A key press has `detail` 0, so the keyboard can confirm.

`Table` takes `stickyActions`. It pins the last column to the right edge of the scroll region, so the actions of a wide row stay in view. A pinned cell is opaque, and a line separates it from the scrolled cells.

`Details` is the one folding section. Its `open` prop sets only the start state. After that, the section follows the user. A later change of the prop does not change it, and a navigation on the same route keeps it.

`RelativeTime` is the one `<time>` element of a time:

- The text is the relative time from `web/src/lib/format.ts`, for example "5m ago".
- `datetime` holds the `toISOString()` form, because the API can send more than 3 fraction digits.
- `title` holds the full time with the zone offset.

A page passes the time of its one clock (`web/src/lib/clock.svelte.ts`) to each `RelativeTime`, so the page has one timer. `Field`, `Select` and `Button` are each 32 px high, so a control and a button in one row line up.

`web/src/app.css` holds the color and spacing tokens under `@theme`. These color tokens are the dark theme. The `:root[data-theme='light']` block gives the same tokens their light values. The `term-grid` utility is the one layout of a list of terms and values. Both of its columns can shrink, so the list fits a phone with any font.

The root layout holds the shell of each screen:

- The app name.
- The nav. The link of the screen on view has `aria-current="page"`. A detail page, such as `/runs/<id>`, marks the link of its list.
- The phase badge. It shows the phase of the run state stream and links to the run screen.
- The theme toggle. Dark is the default. The toggle stores the choice in the `localStorage` of the browser. The head script of `web/src/app.html` shows a stored light choice before the first paint.

Limitation: blocked storage gives the dark theme at each load.

The run screen at `/` shows these parts, from top to bottom:

1. The control bar: Start, Stop, Stop now and the presets control. A line under the buttons tells the two stops apart. The run error and the errors of a refused request show under that line.
2. The status: four cards, then the tables of the workers, the failures and the proxy cooldowns. The cards are the accounts, the success rate, the accounts per hour and the bandwidth per account. Before the first run, one line replaces the cards and the tables. The workers table shows only until the run ends. While a run in progress has no worker, one line replaces that table. The two other tables show only when they have rows.
3. The chart of the run. It takes the width of its box. The time labels use a round step, such as 2 minutes or 1 hour. The bound is one label for each 90 px of the width, but the bound is never below two labels. The chart uses the finest round step in the bound. When that step gives fewer than two labels, the chart keeps every k-th tick of the next finer step, in the bound. For example, 2-minute labels become 4-minute labels. When the year step gives too many labels, the chart keeps every k-th year in the same way. The finest step is 1 second, so the chart shows at least two labels when the run is 2 seconds long or longer. A shorter run shows one label or none.
4. The config form, in folding sections. Only the run settings start open. A closed section shows a digest of its values.

The page holds one edited copy of the config. `web/src/lib/run/edited.ts` gives it to the control bar and the config form through a context. Start sends the copy. Save sends `PUT /api/config` with the copy. A refused save shows its errors in the form. After a save, the form clears the typed IMAP password and shows the stored placeholder. Save stays on during a run, because a run keeps the config that it started with.

The presets control (`web/src/lib/run/Presets.svelte`) has these parts:

- A preset select with "Load" and "Delete". Load fills the edited config from the list. It clears a typed IMAP password, so Start uses the password of serve. Delete asks for a confirm in the page.
- A name field with "Save as preset". It sends `POST /api/presets` with the name and the edited config.

`web/src/routes/+page.ts` loads the config and the presets together.

The mailbox check (`web/src/lib/run/MailboxCheck.svelte`) sends an email section to `POST /api/checks/mailbox`. Serve runs the login check of a run start on it. The check shows in two places:

- The Mailbox section of the config form sends the email fields of the form. So the operator can check the typed values before a save.
- The Checks screen sends the email section of the saved config, with no IMAP password. So serve uses its own password. When the saved config has no mailbox, Check mailbox is off there. A line then tells the operator to set the mailbox on the run screen and save.

The Logs screen at `/logs` shows the lines of the log stream in a virtual list. A line of a new run clears the list. With no line, the screen shows "No run log yet." with a link to the run screen and a link to the Runs screen. It then shows no filters, no count and no list.

The header holds the download link of the log file of the run. Under it, a link to the run and the line count show, for example "250 lines". When a filter has a value, the count reads "56 of 250 lines".

The filters are the level, a text, the component, the worker, the account and the email. The level and the text are in one bar. The other four are in the folding section "More filters". Clear shows while a filter has a value. When no line matches, the screen shows "No line matches these filters." and a button that clears the filters. The email filter selects each line with the `worker` and the `account` of a record line that carries that email.

A line shows these parts:

- The clock time, for example `16:47:11.417`. Its `title` holds the whole time text of the line.
- The level. DEBUG has the muted color, WARN has the warning color and ERROR has the danger color.
- The message and the attributes. A byte attribute, such as `browser_bytes_sent`, shows in binary units, for example `530.5KiB`. Its `title` holds the raw value.
- On a record line, a Steps button. It expands the line to its steps.

The box of the list grows with its lines, up to 75% of the height of the view. Then the list scrolls in the box. The list opens at the last line. While the view is at the end, each new line scrolls into view.

Limitation: the text filter matches the raw byte value, not the shown text. So a search for `530.5KiB` matches no line.

Limitation: the IMAP lines carry `worker` and no `account`, so the account filter and the email filter miss them. A retry keeps its worker and its account number, so the email filter also selects the earlier retries of that account number.

The Runs screen at `/runs` lists the runs of the store, newest first, in pages. The rows do not wrap. A row shows these values:

- The id, which links to the run detail.
- The start as a relative time, and the duration. For a run in progress, the duration runs to the clock of the page, which ticks each 30 s.
- The status: an "In progress" badge, or an "Error" badge with the run error as its `title`. A run with a clean end has no badge.
- The attempts, the accounts, the unconfirmed accounts, the success rate and the failures by category. The accounts count links to the accounts of the run on the Accounts screen.
- The source.

The Status, Unconfirmed and Failures columns show only when a run of the page has a value for them. `runColumns` of `web/src/lib/runs/summary.ts` gives these columns. An empty store shows "No runs yet." and a link to the run screen, with no table.

Limitation: the column rule reads one page, so a column can show on one page and hide on the next.

The run detail at `/runs/<id>` has these parts:

- The header: the id, the status badge, the accounts link and the log download. A run with a clean end has an "Ended" badge.
- The run error in the danger color, when the run has one.
- Three lists. Timing holds the source, the start, the end and the duration. The start and the end show as the full time and the relative time. Results holds the counts, the success rate and the failures by category. Traffic holds the bytes of each phase with a byte, or "None".
- The config: the digest of each of the five sections, then the folding section "All values". It starts closed. It holds one row for each dotted key, from `web/src/lib/runs/summary.ts`.

The Accounts screen at `/accounts` lists the accounts of the store, newest first. The filters live in the page query, and a GET form submits them. `web/src/lib/accounts/query.ts` builds the API query from the page query. It drops each empty value. It sends each time of the form as RFC 3339 in UTC, because the form gives a local time with no zone. The export links and the next page link use the same module. The list has these parts:

- The link "Unconfirmed accounts". It selects the accounts to check by hand.
- The filter bar: the search of the email or the username, Unconfirmed, 2FA, Filter and Clear. The folding section "More filters" holds the run, the proxy and the two creation times. It starts open when the page query sets one of these four filters. A closed section still sends its fields.
- The total line. It shows `total` of the answer, with "matching" when the page query sets a filter. The export links follow it.
- The table. The cells do not wrap. Created is a relative time. 2FA is an "On" or "Off" badge. Status is an "Unconfirmed" badge, or empty for a confirmed account.
- The Run column and the Proxy column show only when an account of the page has the field.

Limitation: the column rule reads one page, so a column can show on one page and hide on the next.

With `total` at 0, the screen shows no table and no export links. An empty store shows "No accounts yet." and a link to the run screen. A filter that matches nothing shows "No account matches these filters." and a link that clears the filters.

The detail at `/accounts/<id>` shows every field of the account:

- The header holds the id, the status badge and the 2FA badge. For an unconfirmed account, it also holds "Mark as checked". The confirm marks the account as checked, then the page reads the account again.
- The Login list holds the email, the username, the password, the TOTP code, the TOTP secret and the backup codes. A long email breaks at the `@` before it breaks inside a word.
- The Origin list holds the creation time, the run with a link to it, the proxy and the bytes of each phase. The creation time shows as the full time and the relative time.
- The password, the TOTP secret and the backup codes stay hidden until a click on their reveal button.
- A copy button puts a secret on the clipboard.
- The live TOTP code counts down its window. At the end of the window, the page reads the account again.

The relative times of the list and of the detail move on in steps of one minute, so their clocks tick each 30 s.

Limitation: on a LAN `-listen` address over http, the browser has no clipboard, so the screen shows no copy button. The revealed text stays selectable.

The Proxies screen at `/proxies` lists the proxies of the store, in id order. The list reads the proxies again when a run ends. The screen has these parts:

- The summary line, for example "20 proxies, 17 enabled". When the list has proxies but no enabled proxy, a warning says that a run stops at start unless "Direct, with no proxy" is on.
- "Test all enabled" tests each enabled proxy from the page, 4 at a time.
- The "Export" link gets the proxy file with the credentials.
- The table. The rows do not wrap. The Proxy cell holds the link to the accounts of the proxy on the Accounts screen. A host wider than 256 px ends in an ellipsis, and the `title` of the link holds the whole `HostPort` form. After the link, an "Auth" badge marks a proxy with credentials, and a "Disabled" badge marks a disabled proxy.
- The actions of each row: enable or disable, test and delete. They are icon buttons in the last column, which the table pins to the right edge. The name of each button holds the action and the proxy, for example "Disable 203.0.113.10:8080".
- The "Import proxies" section. The import takes pasted text or a file. The section starts open when the list is empty, and closed when the list has proxies.

An optional column shows only when a listed proxy has a value for it. The optional columns follow Proxy in this order:

- Test: after a test, a badge with the result and the time. A failed test also shows its error. The column is next to Proxy, so the result shows next to its proxy in a wide table.
- Attempts, Success rate and Bytes: a proxy with an attempt.
- Last attempt: a proxy with an attempt. The cell is a relative time on the one clock of the page.
- Last failure: a proxy with a failed attempt.
- Cooldown: during a run, the cooldown of the proxy from the run state stream, as a warning badge.

Limitation: a cooldown or a test adds its column, so the columns move during a run or a test. This fails toward a moving layout, never toward a hidden value.

Limitation: at a phone width, the Proxy column is wider than the space left of the pinned actions. So the operator scrolls the table to see the Test column.

Limitation: the confirm step widens the pinned column, so "Confirm delete" can land under the mouse. A slow second click then deletes. That is two deliberate clicks, and the guard against a double click still holds.

With no proxy, the screen shows "No proxies yet. Import a proxy file below." and the open import section. It shows no table, no "Test all enabled" and no export link.

`web/src/lib/proxies/` holds the import form, the pool of "Test all enabled" and the match of a cooldown to its proxy. Its `testView` gives the badge and the error text of a test. Its `proxyColumns` gives the optional columns from the listed proxies, their cooldowns and their tests. `web/src/lib/ui/TextArea.svelte` is the wrapper of the import text. The config form of the run screen shows a `direct` checkbox for `proxy.direct`.

The Checks screen at `/checks` has two sections:

- Chrome: a "Found" or "Not found" badge, then the installed Chrome version and the request shape major. A badge after the version shows "Matches" or "Differs". When the installed major differs from `ShapeChromeMajor`, the section also shows a warning with a triangle icon. The CLI WARN uses the same condition. With no Chrome, the section shows the error in place of the version.
- Mailbox: the digest of the mailbox of the saved config, and the mailbox check on that config.

TypeScript is 6.0.3, not 7. TypeScript 7 has no compiler API, so openapi-typescript fails on it. svelte-check and typescript-eslint also do not support it. Move to TypeScript 7 when these three tools support it. The peer of openapi-typescript is TypeScript 5, so one `overrides` entry in `web/package.json` gives it 6.0.3.

Vitest is 5.0.5. `web/patch-stryker-runner.ts` holds two edits of the installed Stryker Vitest runner. It is the `postinstall` script, so each `npm ci` runs it. Each edit must match its text one time, before or after the edit. Any other text fails the script, and `npm ci` fails with it. The script writes no file until every edit passes, and a second run changes nothing.

The first edit changes the test name join to ` > ` in two files of the runner. The runner joins the names of a test and its suites with a space, and Vitest 5 matches the names joined with ` > `. Without the edit, a test inside a `describe` selects no test, and its mutants survive. A runner release that carries stryker-mutator/stryker-js#6214 changes the text, so `npm ci` fails on the update pull request of that release. Then delete this edit, and run the mutation gate. The gate must still kill the mutants of the tests inside a `describe`.

The second edit adds a check to `run()` of the runner, before its `errorsSet` check. The runner reads only the test results. Without the check, a test file that fails to import runs none of its tests, and their mutants survive. The check throws when no test failed and a test file of the run failed. The error names each such file and its first error. The dry run and each mutant run use `run()`.

On a throw, Stryker restarts the runner process and runs the mutant again. It makes two attempts in total. So a mutant whose second attempt imports every file gets its real result. A test file that fails to import in the dry run fails the Stryker run.

A runner release that carries stryker-mutator/stryker-js#6269 changes the text, so `npm ci` fails. #6269 returns an error result, and Stryker does not retry an error result. So delete this edit only when that release also runs the mutant again.

Limitation: an import failure on both attempts gives `RuntimeError`, which the gate does not count and Stryker leaves out of its score. The cause can be the host or a mutant that breaks a module's top level. This fails toward one unmeasured mutant.

Delete the script and the `postinstall` entry only when the script holds no edit. patch-package is not the fix, because its `braces` dependency fails `npm audit --audit-level=high`.

### Set-up

- The Windows host needs Node 26.12.1 for the web commands. Install it with nvm-windows: `nvm install 26.12.1`, then `nvm use 26.12.1`.
- Before the first `go run ./cmd/creator -mode serve` of a clean clone, run `npm --prefix web ci` and `npm --prefix web run build`. Serve refuses to start with no build.
- The binary embeds the build. After a change in `web/src`, build again.
- `npx --prefix web playwright install chromium` installs the browser of the tests.
- `make spa` runs `npm --prefix web ci` and `npm --prefix web run build`. `make build` and `make release` run `make spa` first.

### Commands

Run each script from the repo root with `npm --prefix web run <script>`, or with `npm run <script>` in `web/`. `npm ci` also runs `postinstall`, which runs `web/patch-stryker-runner.ts`. Then it runs `prepare`, which runs `svelte-kit sync`.

- `dev`: starts the Vite dev server. It sends each `/api` request to serve on `127.0.0.1:8080` with no header change. The page and the API share one origin, so the request checks of serve pass. Start `creator -mode serve` first.
- `build`: writes the SPA to `web/dist/spa`.
- `gen:api`: writes `web/src/lib/api.gen.d.ts` from the spec. Do not edit that file.
- `check`, `lint`, `format:check` and `knip`: the static gates.
- `test:unit`: runs the component tests with coverage. The report is `web/coverage/coverage-final.json`.
- `test:e2e`: runs the end-to-end tests. `CREATOR_BIN` must name a built binary, or the run stops with an error. The run starts `$CREATOR_BIN -mode serve` on a free port in a temp dir. It sets `CHROME_PATH` to the Chromium of Playwright.
- `mutate`: runs StrykerJS. The report is `web/reports/mutation/mutation.json`.

### Gates

- `svelte-check --fail-on-warnings`: a type error or a Svelte a11y warning fails.
- ESLint with `--max-warnings 0`: `strictTypeChecked`, `stylisticTypeChecked` and eslint-plugin-svelte. The lint is type-aware, also in the `.svelte` files.
- The complexity rules copy the limits of `.golangci.yml`:
  - `complexity` 15, for cyclop.
  - `max-lines-per-function` 60 and `max-statements` 40, for funlen.
  - `sonarjs/cognitive-complexity` 20, for gocognit.
  - `max-depth` 4, for nestif.
- The test files (`*.test.ts` in `src/` and `e2e/`) are exempt from each complexity rule except `max-depth`. The Go test files are exempt from cyclop, funlen and gocognit in the same way.
- Split a function that a complexity rule reports. Never put a disable comment on it.
- Limitation: `max-depth` only approximates nestif, and no ESLint rule matches maintidx.
- `prettier --check .`. `web/.prettierignore` lists the generated types and the tool outputs.
- knip with its default plugins. Its Playwright plugin is off, because the Playwright config throws with no `CREATOR_BIN`. `web/knip.json` lists `playwright.config.ts` and the e2e files as entries.
- Each e2e test compares the screen with the API answer and runs axe on the screen. The axe helper checks the screen in the dark and the light theme. The run test starts a run with a local TCP listener that never answers as the mailbox. The run waits in its mailbox check until the test stops it, so it starts no Chrome and sends no request to Jagex. After the stop, the run test checks the `run started` and `run ended` lines on the Logs screen. It also compares the downloaded log file with the answer of `GET /api/runs/{id}/log`. Then the run test checks the Runs screen and the run detail. It saves the config and checks the saved value after a reload. It sends the mailbox check to a closed port and compares the error with the API answer. The Checks test reads the badges and the mailbox digest. It expects the warning when `mismatch` is true, and no warning when it is false. It does not click Check mailbox, because the run test saves a mailbox host that looks real. It checks that the button is off with no saved mailbox and on with one. The accounts test reads the accounts of `web/e2e/accounts.jsonl`. `web/playwright.config.ts` copies that seed into the temp dir when it creates the dir, so serve imports it into its new database. The shell test reads only the id of a seeded account, for the path of its detail. No other test reads the seeded accounts. The run removes the temp dir when the runner exits, after serve stops. An axe violation fails the test.
- A control in a closed `<details>` section takes no fill or click, and `getByRole` does not find it. So a test calls `openSections` of `web/e2e/helpers.ts` before it fills or clicks a control of the config form or of "More filters".
- `shownRows` of `web/e2e/helpers.ts` gives the text of each cell of a table. It joins each run of white space into one space, so the badges of a cell read as words. A cell whose child is a `<time>` element gives its `datetime` value, because the shown relative time changes with the clock. A test compares that value with the `toISOString()` form of the API time.
- The presets test runs its steps in order. It saves a preset, finds it in the list and loads it. Then it saves again under the same name and deletes the preset with the confirm. It runs axe at each step. Its preset name is unique to the file, and it deletes the preset that it saves.
- The shell test checks the active nav link of each screen and of an account detail. It compares the phase badge with `GET /api/run`. It checks that a light choice stays after a reload. At a 390 px wide viewport, it checks that no screen scrolls sideways. A wide table scrolls only in its own region.
- "Testing Notes" holds the web coverage gate, the Stryker half of the mutation gate, the type drift check and `npm audit`.

Limitation: `web/stryker.config.json` sets `timeoutMS` to 30000. A host under load can pass that bound. Such a mutant fails toward a false `Timeout`, never toward a false pass.

`web/vite.config.ts` sets the test timeout to 10 s. It also sets a bound of 5 s on each `expect.element` and `expect.poll` wait. These values follow the guard rule of "Testing Notes". So a mutant that makes a test wait fails before the timeout of Stryker. The `actionTimeout` of the Playwright provider is necessary for the bound of `expect.element`. Without it, `expect.element` waits for the rest of the test timeout.

Limitation: an explicit `timeout` option, or a per-test timeout over 10 s, bypasses these bounds. This fails toward a slow mutation run.

The Playwright provider of the unit tests fixes the locale `en-US` and the zone `Asia/Kolkata`. So a test states an exact local time. The zone is not UTC, so a test that assumes UTC fails.

Limitation: the e2e config finds a free port and closes it, then serve binds it. Another process can take the port in between. The run then fails, never passes.

Limitation: a hard kill of the runner skips the exit listener and leaves the temp dir. This fails toward a stale dir.

---

## Email Verification

The email provider interface (`internal/email/provider.go`):

```go
type Provider interface {
    // Address returns the address of the mailbox with the local part local
    Address(local string) string

    // WaitForVerificationCode polls for a verification email and returns the verification code
    WaitForVerificationCode(ctx context.Context, address string, since time.Time, timeout time.Duration) (string, error)
}
```

`IMAPProvider`, with IDLE support, is the only implementation. The config loader fails when a required IMAP field is empty. The run checks the mailbox login at start.

---

## Error Handling Patterns

```go
// The function that does the work names it.
// A caller wraps only to add a name the callee's text lacks.
// It never repeats the callee's operation.
result, err := c.Page.Navigate(ctx, navArgs)
if err != nil {
    return fmt.Errorf("navigating to %s: %w", url, err)
}

// Sentinel errors are defined in their respective packages:
// browser/errors.go:  ErrChallengeTimeout, ErrLoadTimeout, ErrProxyConnection, ErrUserAgentOverride
// jagexapi/errors.go: ErrRegistrationFailed, ErrRateLimited
// email/errors.go:    ErrEmailTimeout, ErrEmailLogin

// Retry logic (jagexapi/retry.go) resends a GET after a transport error or a 5xx, with exponential backoff
// It sends every POST once, so the answer to a POST is the answer to its only send
// It does NOT retry on context cancellation
// A 429 returns once as ErrRateLimited, with no retry

// A status error carries the response summary (jagexapi/errors.go describeResponse):
// the status, the headers retry-after, cf-mitigated, server, cf-ray and
// content-type when they are set, and the body length. It never holds the URL.
// The first 512 bytes of the body go to a DEBUG line.
```

---

## Code Style & Conventions

- **Go** at the `go` line of go.mod — use `range` over integers, `slices` package, etc. where appropriate.
- **`errors.Is` / `errors.AsType`** for error checking, never `==` comparison on wrapped errors.
- **No `init()` functions.** All initialization is explicit.
- **No `interface{}` or `any` when a concrete type is known.**
- **Struct field tags:** Use `json` tags on all serialized structs. Use `yaml` tags for config structs.
- **Naming:** Follow Go conventions. Exported names are PascalCase. Unexported are camelCase. Acronyms are all-caps (`URL`, `HTTP`, `CDP`).
- **Test files:** `_test.go` suffix. Use table-driven tests where appropriate.
- **Comments:** All exported types and functions get doc comments. Internal functions get comments when the "why" isn't obvious.

---

## Things to NOT Do

- **Do NOT use `chromedp`.** We are using `mafredri/cdp` directly. They are different libraries.
- **Do NOT use `net/http` for making external requests.** Always use `github.com/sardanioss/httpcloak/client` types (`client.Client`, `client.Request`, `client.Response`, `client.Cookie`). The `net/http` import is acceptable only for internal utilities. The forward proxy and the local API server of serve mode (`cmd/creator/serve*.go`) are such utilities. depguard denies `net/http` in every other file.
- **Do NOT use `DOM.querySelector` to find the Turnstile iframe.** It cannot enter a shadow root. Use a `DOM.getDocument` snapshot with `pierce` true.
- **Do NOT hardcode selectors for Cloudflare Turnstile internals.** These change. Keep them in config or constants that are easy to update.
- **Do NOT log sensitive data** (passwords, full proxy credentials, email passwords) at INFO level. Use DEBUG level, which should be off by default.
- **Do NOT use `os.Exit` except in `main()`.** Return errors up the call stack.
- **Do NOT mix concerns.** The browser package should know nothing about Jagex. The jagexapi package should know nothing about CDP. The cookies package bridges them.

---

## Testing Notes

- Unit tests for `cookies/convert.go`, `account/generator.go` — these have no external deps.
- CI runs `scripts/coverage-gate.sh`. It runs the tests, and it fails when a change has more uncovered statements than its base commit. It also fails when the head tests leave a path in their user cache dir or change the tree. `make cover` runs the same script against `git merge-base origin/main HEAD`.
- The tree check of the coverage gate compares two lists of every path in the tree, ignored paths included. It takes one list before the tests and one after them. A file has its mtime and its size in the list. A dir, the root included, has its mtime. So the gate sees a new, changed or deleted file or dir, also when the clock steps back. It also sees a file that a test creates and then deletes, because the mtime of its dir changes. `file_state` in `scripts/file-state.sh` is the one home of the list. The web coverage gate uses it too.
- CI runs `scripts/web-coverage-gate.sh` in the `ci` job. It runs `npm --prefix web run test:unit` and counts the uncovered statements in `web/coverage/coverage-final.json`. The gate counts a statement with a hit count of 0 as uncovered.
- The web coverage gate runs the same steps on the base in its own worktree, after `npm ci` and the Playwright install there. A base with no `web/package.json` has 0 uncovered statements. The gate takes the base argument of `coverage-gate.sh`, with the same default.
- The web coverage gate fails when the head count is above the base count. It then prints each uncovered statement of head as `file:line`. So every statement of `web/src` stays covered. Cover a new file with a test, never with a coverage exclude entry.
- The web coverage gate also fails when the head tests change the tree. It uses the list of the coverage gate. The test run itself writes in four dirs, so the check skips them:
  - `web/coverage`: the report.
  - `web/.svelte-kit`: the sync of the SvelteKit plugin.
  - `web/node_modules/.vite`: the Vite cache.
  - `web/node_modules/.vite-temp`: Vite writes the bundled `vite.config.ts` there and deletes it again.

  The rest of `web/node_modules` stays in the check. A dir that holds a skipped dir, `web/` and `web/node_modules/`, has no mtime in the list, because a write in the skipped dir changes it.
- `web/vite.config.ts` sets `fsModuleCache` to false. The module cache of Vitest writes `web/node_modules/.vitest-cache`, and the tree check of the web coverage gate refuses that write.
- Limitation: the web coverage gate does not see a write in a skipped dir. It does not see a file that a test creates and then deletes directly in `web/` or `web/node_modules/`.
- Limitation: when `web/node_modules` exists, the head run of the web coverage gate uses it as it is. A stale dir on a dev host fails toward a local run on stale dependencies, never a false CI pass. In CI, the `ci` job runs `npm ci` before the gate.
- Limitation: a change that removes a web test and removes an equal number of uncovered statements in another place passes.
- A function that cannot run in a test carries `// coverage-ignore: <reason>` on its opening-brace line. The reason is `needs a live Chrome`, `starts an OS process` or `needs a console`. The coverage gate fails when the tests of the same package run every statement of an annotated function. Each OS job checks only the files that it builds.
- Annotate a function only when its body is the external call plus error wrapping or logging, or when its success path cannot complete without the resource. A function with logic that a fake can drive gets a test.
- The coverage gate and gremlins skip `internal/api/api.gen.go`, the generated code. Each gate names the file in its own config line. A second generated Go file adds its own line in each gate. The `mutate` list of `web/stryker.config.json` skips the generated `web/src/lib/api.gen.d.ts`.
- A `web/src/lib/*test/` dir, such as `streamtest/` or `configtest/`, holds the shared helpers of the web tests. Only test files import it. It has its own test, as a Go `*test` package does. A helper dir that one test alone imports, such as `clocktest/`, has no test of its own, because that test covers it. The web coverage gate and Stryker judge it as any other file.
- CI runs `scripts/mutation-gate.sh`. It has two halves. gremlins mutates the Go code, and Stryker mutates `web/src` through `npm --prefix web run mutate`. The gate fails when the tests still pass or time out on a mutant of a covered line. It prints each such mutant as `file:line TYPE STATUS`, with the file relative to the repo root. `make mutate` runs the same script. It needs Linux, `jq` and the Node of `web/.nvmrc`.
- A gremlins survivor is `LIVED` or `TIMED OUT`. A Stryker survivor is `Survived` or `Timeout`. A Stryker `NoCoverage` mutant goes to the web coverage gate, as a gremlins `NOT COVERED` mutant goes to the Go coverage gate.
- gremlins runs on a copy of the working tree. The copy holds the files of `git ls-files --cached --others --exclude-standard`, so it has no ignored path, such as `web/node_modules`. So `make mutate` tests the tree that the developer has.
- The mutation gate fails when gremlins kills no mutant. It also fails when Stryker kills no mutant.
- Limitation: when `web/node_modules` exists, Stryker uses it as it is. A stale dir on a dev host fails toward a local run on stale dependencies, never a false CI pass. In CI, the dir comes from `npm ci` of the same commit.
- A mutant that no test can kill carries `// mutation-ignore: <TYPE>[, <TYPE>]: <reason>` on its source line. In Svelte markup, the opener is `<!--` in place of `//`. `<TYPE>` is the type that the gate prints. A gremlins type is upper case, for example `CONDITIONALS_BOUNDARY`. A Stryker type is CamelCase, for example `EqualityOperator`.
- A waiver covers only the listed types. The gate fails when a listed type has no surviving mutant on the line, and prints `file:line TYPE stale waiver`. The stale check reads the `.go` files and the `.ts` and `.svelte` files under `web/src`. Prefer a test or a simpler expression to a waiver.
- A `Stryker disable` or `Stryker restore` comment under `web/src` fails the mutation gate. Stryker reports a disabled mutant as `Ignored` and never runs it, so the stale check cannot see it. Use a waiver.
- Limitation: the disable check also refuses a comment that only names the directive.
- A test that asserts a duration runs inside `synctest.Test` and asserts the exact value. A test on a real socket asserts no duration tighter than its guard. The test waits on the event under a guard. The guard fails the test when it fires. A guard is at least 2s and at most 10s. The 10s cap keeps a guard below the mutation gate's per-mutant timeout. `synctest.Test` goes inside each `t.Run`, because a bubble must not call `t.Run` or `t.Parallel`. A guard also bounds the real work of the test, and a runner disk can be slow. So a test under a guard does few disk syncs.
- Limitation: no gate checks the 10s cap or the sync count. Review holds both rules.
- `internal/waitcheck` fails a test that waits on the real clock with no guard, outside a `synctest.Test` bubble. Its package doc lists the rules and the approximations. Its `net.Pipe` rule asks for more than the rule above. A guard does not satisfy it. A test that opens a pipe runs in a bubble.
- Limitation: `internal/waitcheck` reads only the test files and the `*test` helper packages that they import. It also reads the non-test files of a `*test` helper package that has its own test files. It does not see a wait in other production code that a test calls. Such a wait passes. Review holds that part.
- Limitation: no gate detects a test that cannot fail. Review holds that rule.
- A test touches no path outside its temp dir. Each surface has one helper:
  - The user cache dir: `setCacheEnv` in `internal/browser` sets `LOCALAPPDATA`, `XDG_CACHE_HOME` and `HOME`.
  - `os.TempDir`: `setTempDir` in `internal/jagexapi` sets `TMPDIR`, `TMP` and `TEMP`.
  - The working dir: the test calls `t.Chdir(t.TempDir())`.
- A test opens its account store in `t.TempDir()` and closes it in `t.Cleanup`, so Windows can remove the dir. Each test package opens its own store, with no shared helper package.
- A test of serve passes the dashboard as a `fstest.MapFS` (`builtDashboard` in `cmd/creator/serve_spa_test.go`). `TestSPA` in `web` holds in a clean clone and in a tree with a build.
- Limitation: the coverage gate cannot see a write under `os.TempDir`, where `t.TempDir` also lives. It cannot see a write in the user cache dir that the test deletes again. It sees a change or a delete of an ignored file only in a tree that holds that file. The dev tree of `make cover` is such a tree. Review holds these shapes.
- CI runs `scripts/vuln-gate.sh`. It runs govulncheck on the whole tree for Linux and for Windows. A red `vuln` result with no related change means a new advisory. To repair it, bump the `go` line or the module in its own pull request. `make vuln` runs the same script.
- CI runs `scripts/deadcode-gate.sh`. It fails when production code has no caller from main. It also fails when a `*test` helper package has no caller in the test of another package. `make deadcode` runs the same script.
- CI runs `go mod tidy -diff`. It fails when go.mod or go.sum is not tidy. Run `go mod tidy` to repair it.
- CI runs golangci-lint with `.golangci.yml` through `scripts/golangci-lint.sh`. The script builds the pinned version from source. `make lint` runs the same script. The complexity linters are cyclop, funlen, gocognit, maintidx and nestif. `.golangci.yml` holds their limits. Split a function that one of them reports. Never put `//nolint` on it.
- golangci-lint runs `dupl` on the test files too. dupl's threshold is in `.golangci.yml`. A test that compares text log lines calls `logtest.New`, the one home of that logger.
- Limitation: no lint reads a log value. Review holds the rule that a line at INFO or above names a request by its path.
- Test an HTTP request at the request-building level with a fake transport (`Client.doFn`). The wire test (`internal/jagexapi/wire_test.go`) runs the real client through a fake CONNECT proxy to a loopback HTTP/2 server. It checks the header fields of each request shape in wire order.
- GitHub Actions cannot run a job, so the owner disabled both workflows. `scripts/local-ci.sh <PR>` runs the four jobs on this host. It posts each result as a commit status on the head of the PR.
  - `scripts/ci-job.sh <job> [base]` holds the steps of each job. The workflows and `local-ci.sh` call it.
  - ci-windows runs in a Windows worktree. ci, vuln and mutation run in a clone in WSL Ubuntu-24.04. Mutation runs last and alone.
  - The PATH of `bash -s` in WSL has no Node. So each Linux job loads nvm, then runs `nvm install` and `nvm use` with the pin of `web/.nvmrc` in the WSL checkout.
  - The protection on main accepts a status from any app: each of the four required checks has `app_id: -1`.
  - Ship a PR in this sequence:
    1. Push and open the PR.
    2. Start `scripts/local-ci.sh <PR>` as a background task and record its PID.
    3. Arm watch-ci.
    4. On a new head, kill that PID, then start the script again.
  - Read a red from the log path in the status description. The description also names the failed step. A long path keeps only its end, and the output of the script gives the full path.
  - A refusal exits 2 before the first status. The script refuses a closed PR, an active workflow, and a head that does not contain the base. So the exit of the task is the signal, not watch-ci.
  - The active-workflow check reads only the paths under `.github/workflows/`. Dependabot lists its updates as the active workflow `dynamic/dependabot/dependabot-updates`, which has no workflow file.
  - Do not run `protect-main --apply` while the workflows are disabled. It sends no `app_id`, so GitHub pins each check to GitHub Actions again.
  - To return to GitHub Actions:
    1. Enable both workflows: `gh workflow enable CI` and `gh workflow enable vuln`.
    2. Send one protection PUT that sets `app_id: 15368` on each of the four required checks. Never omit `app_id`, because GitHub then pins each check to the app that reported it last.
  - The workflows install the Node of `web/.nvmrc` through `actions/setup-node`, pinned by commit SHA.
- `scripts/ci-job.sh` runs the web steps of each job:
  - Each job that uses Node first checks that `node --version` is `v` and the pin of `web/.nvmrc`. A host with another Node fails at the `node` step.
  - `ci` runs these steps in order:
    1. The Go steps. They run before the first npm step, so they see the tree of the checkout alone, as in `ci-windows`. The embed builds with the tracked `.gitkeep` alone.
    2. `npm --prefix web ci`.
    3. `api_drift`. It also runs `npm --prefix web run gen:api` and checks `web/src` in the same `git diff --exit-code`.
    4. `check`, `lint`, `format:check` and `knip`.
    5. The web coverage gate.
    6. `npm --prefix web run build`. Only the e2e binary embeds the build, so the build runs with the e2e steps, after the web gates.
    7. The Playwright Chromium install.
    8. A `go build` of the binary into a temp dir, then `test:e2e` with `CREATOR_BIN` set to that binary.
  - `vuln`: after govulncheck, `npm --prefix web ci` and `npm --prefix web audit --audit-level=high`. The bundled libraries of the dashboard are dev dependencies, so the audit reads the whole tree.
  - `mutation`: the mutation gate, with both halves.
  - `ci-windows` needs no Node. The embed of `web/dist` builds with the tracked `.gitkeep` alone.
- `.github/dependabot.yml` opens weekly update pull requests for npm in `/web`, gomod and github-actions. One group holds the minor and patch updates of each ecosystem. A release waits 7 days before its pull request.
- While both workflows stay off, a Dependabot pull request gets no check. Run `scripts/local-ci.sh <PR>` on each one.
- Limitation: any token with repo scope can post a passing status.
- Limitation: on Windows, the child processes of a killed run survive. These orphaned jobs post nothing, because only `local-ci.sh` posts. They only cost CPU. A gremlins `TIMED OUT` or a Stryker `Timeout` that their load causes fails the gate, so it fails closed. A `kill <PID>` also runs the exit trap of the script. The trap posts `error` on the old head for each job with no result, and it removes the checkouts of the run.
- Limitation: the daily vuln run stops while the workflows are disabled. A new advisory shows first on the vuln status of the next PR. This is a late finding, never a false pass.
- Gate surface:
  - `.golangci.yml`, `.golangci-lint-version`, `.testcoverage.yml`, `scripts/`, `internal/waitcheck/` and `Makefile`.
  - `web/package.json`, `web/.npmrc`, `web/.nvmrc`, `web/tsconfig.json`, `web/vite.config.ts`, `web/eslint.config.js`, `web/.prettierrc.json`, `web/.prettierignore`, `web/knip.json`, `web/playwright.config.ts`, `web/stryker.config.json` and `web/patch-stryker-runner.ts`.
  - `.github/dependabot.yml`.

  A diff that touches one of these gets the gate-weakening refuter at ship time.

---

## Dependencies Summary

```
github.com/mafredri/cdp           — CDP protocol bindings
github.com/sardanioss/httpcloak    — TLS-fingerprinted HTTP client (TLS-only mode: the requests set every header)
github.com/emersion/go-imap        — IMAP client for email verification
gopkg.in/yaml.v3                   — YAML config parsing
golang.org/x/net                   — SOCKS5 client of the forward proxy, and the HTTP/2 frame reader of the wire test
golang.org/x/sync                  — errgroup (concurrent workers) and semaphore (Chrome slots)
golang.org/x/sys                   — Windows version-resource reads and the console mode of stderr
golang.org/x/term                  — Terminal check of stderr on Unix
github.com/lmittmann/tint          — The colored log view on a terminal
modernc.org/sqlite                 — SQLite driver for the account store (pure Go, no cgo)
github.com/oapi-codegen/runtime    — The path and query parameter binding of the generated API code
```

A new dependency needs the approval of the owner. The owner approves only a module that is the modern standard for its job. No other module enters go.mod.

`web/package.json` holds the front-end dependencies, each with an exact pin. The same approval rule applies to a new npm package.