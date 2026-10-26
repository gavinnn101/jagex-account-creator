# Jagex Account Creator

A Go tool that creates Jagex accounts. Chrome loads the Jagex page and solves the Cloudflare challenge. Then an HTTP client with a Chrome TLS fingerprint uses the Chrome cookies and does the registration.

## Features

- Chrome, controlled over CDP, gets the Cloudflare `cf_clearance` cookie and clicks the Turnstile checkbox when the page shows a challenge.
- The HTTP client ([httpcloak](https://github.com/sardanioss/httpcloak)) copies the TLS and HTTP/2 fingerprint, the user agent and the client hints of that Chrome.
- The tool does each registration screen: email, verification code, display name and password.
- It can set up TOTP (2FA) and save the secret and the backup codes.
- It reads the verification code from an IMAP mailbox.
- Each attempt takes a proxy from the proxy list. Chrome and the HTTP client each connect through a local forward proxy that counts the bytes.
- Workers run in parallel. A failed attempt retries after a backoff.
- A local dashboard starts and stops runs and shows the live state, the logs, the run history, the accounts and the proxies.
- A SQLite database holds the accounts, the proxy list, the run presets and the run history.

## Requirements

- Google Chrome or Chromium. The tool finds it on the usual install paths, or on `PATH`. Set `CHROME_PATH` to use a different binary.
- Go, at the version of the `go` line in `go.mod`.
- Node 26.12.1, the pin of `web/.nvmrc`, to build the dashboard.
- An IMAP mailbox that gets the mail of every address of each domain in `imap_domains` (a catch-all). Each attempt takes one of these domains at random and makes a new address `<12 random letters and digits>@<domain>`. The tool logs in to the mailbox over TLS and reads the verification code from `INBOX`.
- Proxies, HTTP or SOCKS5. A run with no enabled proxy stops at start, unless `proxy.direct` is `true`.

## Build

```bash
npm --prefix web ci
npm --prefix web run build
go build -o creator ./cmd/creator
```

On Windows, use `-o creator.exe`. The commands below then run as `./creator.exe`. On a host with `make`, `make build` runs the same three commands.

The binary embeds the dashboard. After a change in `web/src`, build the dashboard and the binary again. Serve mode refuses to start when the binary has no dashboard build.

## Quick start

1. Copy the template: `cp config.yaml config.local.yaml`.
2. In `config.local.yaml`, set `email.imap_host`, `email.imap_user`, `email.imap_pass` and `email.imap_domains`.
3. Start serve mode: `./creator -mode serve`.
4. Open http://127.0.0.1:8080.
5. On the **Proxies** screen, open "Import proxies". Paste your proxy lines or pick a file, then click Import.
6. On the **Checks** screen, check that the Chrome badge shows "Found". Click "Check mailbox".
7. On the **Run** screen, set "Account count" and "Max workers" in "Run settings", then click Start.

The **Logs** screen shows the log lines of the run. The **Runs** screen shows the run history. The **Accounts** screen shows the accounts and their exports.

## Configuration

`config.yaml` is the template. Its comments name each key. Each mode reads `config.local.yaml` unless `-config` names a different file. Git ignores `config.local.yaml`, so the IMAP password stays out of the repo. Save on the dashboard writes to the file that `-config` names.

The keys to set:

| Key                         | Use                                                                                    |
| --------------------------- | -------------------------------------------------------------------------------------- |
| `email.imap_host`           | The IMAP server, for example `imap.example.com`                                        |
| `email.imap_user`           | The login of the mailbox                                                               |
| `email.imap_pass`           | The password of the mailbox                                                            |
| `email.imap_domains`        | The list of the domains of the new addresses, for example `[example.com, example.org]` |
| `database`                  | The path of the SQLite database. The default is `creator.db`                           |
| `proxy.direct`              | `true` runs with no proxy and ignores the proxy list                                   |
| `concurrency.max_workers`   | The number of attempts in parallel                                                     |
| `concurrency.account_count` | The number of accounts to create. `-1` is no limit                                     |

Each entry of `email.imap_domains` is an ASCII domain name with at least one dot, in any case, and no entry repeats. Write a non-ASCII domain in its `xn--` form.

## CLI use

The full mode creates accounts with no dashboard:

```bash
./creator
```

| Flag        | Default             | Description                                                                                                                                                   |
| ----------- | ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `-config`   | `config.local.yaml` | The path of the config file                                                                                                                                   |
| `-mode`     | `full`              | `full` (create accounts), `challenge` (Turnstile debug only), `export` (write the stored accounts as JSONL) or `serve` (the dashboard and its local HTTP API) |
| `-out`      | (none)              | Export mode: the path of the new JSONL file. The file must not exist                                                                                          |
| `-listen`   | `127.0.0.1:8080`    | Serve mode: the address of the dashboard and the HTTP API                                                                                                     |
| `-debug`    | (from config)       | Turn on debug logging and screenshots. Serve mode refuses it                                                                                                  |
| `-headless` | (from config)       | Run Chrome in headless mode. Serve mode refuses it                                                                                                            |
| `-count`    | (from config)       | The number of accounts to create, `-1` for no limit. Serve mode refuses it                                                                                    |

Export mode writes every stored account to a new file. It needs a full valid config, as the full mode does:

```bash
./creator -mode export -out accounts-export.jsonl
```

The first Ctrl+C stops the run softly: no new attempt starts, and the attempts past the password step finish and write their accounts. A second Ctrl+C stops at once. An attempt that the second Ctrl+C stops after the password step still writes its account, as unconfirmed.

In serve mode, Stop and Stop now on the Run screen do the same two stages.

## Proxies

The database holds the proxy list. Import it on the Proxies screen. Each line of a proxy file is `[scheme://]host:port[:user:pass]`:

- The scheme is `http`, `socks5` or `socks5h`. A line with no scheme uses `http`.
- The import skips empty lines and lines that start with `#`.
- A bad line stops the whole import, and no proxy changes.
- The import enables each new proxy.
- A known proxy with new credentials gets them and keeps its enabled state. When two lines name one proxy, the last line wins.

A run uses the enabled proxies as it read them at its start. A change during a run applies at the next start.

To move an existing proxy file into a new database, put it beside the database as `proxies.txt` before the first start. The tool imports it one time. After that, the file is a stale copy.

## Output

The SQLite database at the `database` path holds each account. The tool creates the file with mode 0600. Export mode writes the accounts as JSONL. The Accounts screen exports the accounts that its filters select, in three formats:

- `jsonl`: one JSON object for each account.
- `csv`: a header row, then one row for each account.
- `userpass`: one `email:password` line for each account. Jagex logs in with the email.

An account is unconfirmed when the tool did not see Jagex confirm its creation or its 2FA activation. For example, a second Ctrl+C can stop the attempt before the confirm. Check each unconfirmed account by hand. With no TOTP secret, the creation is unconfirmed. With a TOTP secret and no backup codes, the 2FA activation is unconfirmed. When the check passes, click "Mark as checked" on its Accounts detail page. When it fails, leave the account unconfirmed. The filter `unconfirmed=false` then leaves it out of an export.

When the tool creates a new database, it imports `accounts.jsonl` from the directory of the database one time. After the import, that file is a stale copy, and the database is the one record.

`internal/api/openapi.yaml` is the contract of the HTTP API of serve mode.

The API has no login. Use a LAN `-listen` address only on a trusted network. Any client that can reach the address can read the passwords, the TOTP secrets and the proxy credentials.

## How it works

1. Takes the healthy proxy with the fewest attempts in flight. When every proxy is in cooldown, the worker waits
2. Waits for a free Chrome slot (`max_browsers`)
3. Checks that the upstream proxy accepts a connection. A failed check puts the proxy in cooldown and ends the attempt
4. Starts Chrome through its local forward proxy
5. Loads the Jagex manage page (`/en-GB/manage?flow=registration`)
6. Builds an httpcloak HTTP client with the browser's UA, client hints and early cookies, and a matching TLS fingerprint. The HTTP client connects through its own local forward proxy and gets the OAuth login link in the background
7. Waits until both Cloudflare passes set `cf_clearance`: the JavaScript detection report and the managed challenge. It clicks the Turnstile checkbox when the page shows a challenge
8. Extracts all cookies from the browser session and closes Chrome in the background. The Chrome slot frees when Chrome exits
9. Imports the cookies into the HTTP client, follows the login link to `/oauth2/auth`, then follows the redirect to `registration-start`
10. Does the registration screens over HTTP: email, verification code, display name check and password
11. Optionally logs the new account in over HTTP and sets up TOTP 2FA, with no second Turnstile solve
12. Saves the account to the SQLite database when Jagex did not refuse the password, also after a later failure
13. Logs one record line for the attempt, on success or failure, and saves the attempt to the run history

## Development

```bash
make test         # go test -race ./...
make cover        # the tests and the coverage gate
make lint         # golangci-lint at the pinned version
make mutate       # the mutation gate. Needs Linux, jq and the Node of web/.nvmrc
make vuln         # the vulnerability gate
make deadcode     # the reachability gate
```

On a host with no `make`, run the command of the target in `Makefile`.

The dashboard has its own scripts. Run each one from the repo root with `npm --prefix web run <script>`: `dev`, `build`, `gen:api`, `check`, `lint`, `format:check`, `knip`, `test:unit`, `test:e2e` and `mutate`. The tests need the Chromium of Playwright: `npx --prefix web playwright install chromium`.

Two waivers exist for code that a test cannot reach:

- A function that cannot run in a test carries `// coverage-ignore: <reason>` on its opening-brace line.
- A mutant that no test can kill carries `// mutation-ignore: <TYPE>[, <TYPE>]: <reason>` on its source line.

GitHub Actions does not run the gates for this repo. The maintainer runs `scripts/local-ci.sh <PR>` on each pull request. The script runs the four CI jobs and posts each result as a commit status on the head of the pull request. A contributor cannot post these statuses. `CLAUDE.md` holds the full gate rules.

## License

GPL-3.0. See [LICENSE](LICENSE).
