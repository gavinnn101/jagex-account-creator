package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"jagex_account_creator/internal/api"
	"jagex_account_creator/internal/config"
	"jagex_account_creator/internal/jagexapi"
	"jagex_account_creator/internal/logtest"
	"jagex_account_creator/internal/waittest"
)

// storedConfig returns the config that config.Read returns for a file with a
// complete email section. An empty pass leaves imap_pass out of the file.
func storedConfig(t *testing.T, pass string) *config.Config {
	t.Helper()
	content := "email:\n  imap_host: mail.example.com\n  imap_user: user\n  imap_domains: [example.com]\n"
	if pass != "" {
		content += "  imap_pass: " + pass + "\n"
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// doAPI sends one request through the request checks to h, with the Host of a
// browser on the same machine.
func doAPI(ctx context.Context, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
	r.Host = "127.0.0.1:8080"
	w := httptest.NewRecorder()
	guardAPI(h).ServeHTTP(w, r)
	return w
}

// decodeBody decodes the JSON body of w into v. It fails the test on a status
// other than want.
func decodeBody(t *testing.T, w *httptest.ResponseRecorder, want int, v any) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status = %d, want %d, body %s", w.Code, want, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("decoding %s: %v", w.Body.String(), err)
	}
}

func newTestAPI(cfg *config.Config) http.Handler {
	return (&apiServer{cfg: cfg, logger: logtest.New(io.Discard)}).handler()
}

func TestGetConfig(t *testing.T) {
	for _, pass := range []string{"stored-secret", ""} {
		t.Run("imap_pass "+pass, func(t *testing.T) {
			w := doAPI(t.Context(), newTestAPI(storedConfig(t, pass)), "GET", "/api/config", "")

			if pass != "" && strings.Contains(w.Body.String(), pass) {
				t.Fatalf("the body holds the IMAP password: %s", w.Body.String())
			}
			var raw struct {
				Email map[string]json.RawMessage `json:"email"`
			}
			decodeBody(t, w, http.StatusOK, &raw)
			if _, ok := raw.Email["imap_pass"]; ok {
				t.Errorf("the email section has an imap_pass key: %s", w.Body.String())
			}
			var got api.Config
			decodeBody(t, w, http.StatusOK, &got)
			want := api.Config{
				Database: "creator.db",
				Chrome:   api.ChromeConfig{Headless: true, Timeout: "1m0s"},
				Email: api.EmailConfig{ImapHost: "mail.example.com", ImapPort: 993, ImapUser: "user", ImapDomains: []string{"example.com"}, ImapPassSet: new(pass != ""),
					Auth: api.EmailAuthPassword, Oauth2: api.OAuth2Config{ClientSecretSet: new(false), RefreshTokenSet: new(false)}},
				Account:     api.AccountConfig{PasswordLength: 16, EnableTotp: true},
				Concurrency: api.ConcurrencyConfig{MaxWorkers: 1, MaxBrowsers: 1, AccountCount: 1, AccountTimeout: "5m0s"},
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("GET /api/config =\n%+v\nwant\n%+v", got, want)
			}
		})
	}
}

// storedOAuth2Config returns the config that config.Read returns for a file whose
// email section logs in with oauth2 and holds the client secret and the refresh token.
func storedOAuth2Config(t *testing.T) *config.Config {
	t.Helper()
	content := "email:\n  imap_host: mail.example.com\n  imap_user: user\n  imap_domains:\n    - example.com\n  auth: oauth2\n" +
		"  oauth2:\n    client_id: client\n    client_secret: stored-secret\n    refresh_token: stored-refresh\n" +
		"    token_url: https://oauth2.example.com/token\n"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestGetConfigOAuth2(t *testing.T) {
	w := doAPI(t.Context(), newTestAPI(storedOAuth2Config(t)), "GET", "/api/config", "")

	for _, secret := range []string{"stored-secret", "stored-refresh"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("the body holds %s: %s", secret, w.Body.String())
		}
	}
	var got api.Config
	decodeBody(t, w, http.StatusOK, &got)
	want := api.EmailConfig{ImapHost: "mail.example.com", ImapPort: 993, ImapUser: "user", ImapDomains: []string{"example.com"}, ImapPassSet: new(false),
		Auth: api.EmailAuthOAuth2, Oauth2: api.OAuth2Config{ClientId: "client", ClientSecretSet: new(true), RefreshTokenSet: new(true), TokenUrl: "https://oauth2.example.com/token"}}
	if !reflect.DeepEqual(got.Email, want) {
		t.Errorf("email of GET /api/config =\n%s\nwant\n%s", jsonOf(t, got.Email), jsonOf(t, want))
	}
}

// postedOAuth2 sets auth oauth2 and posts a complete oauth2 section.
func postedOAuth2(c *api.Config) {
	c.Email.Auth = api.EmailAuthOAuth2
	c.Email.Oauth2 = api.OAuth2Config{ClientId: "client", RefreshToken: new("posted-refresh"), TokenUrl: "https://oauth2.example.com/token"}
}

// postedConfig returns the body of GET /api/config for cfg, changed by edit.
func postedConfig(t *testing.T, cfg *config.Config, edit func(*api.Config)) string {
	t.Helper()
	c := apiConfig(cfg)
	edit(&c)
	body, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestValidateConfig(t *testing.T) {
	keep := func(*api.Config) {}
	// The relative and the absolute path name one file, but the check compares the text.
	absDatabase, err := filepath.Abs("creator.db")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		storedPass string
		edit       func(*api.Config)
		want       api.ValidationResult
	}{
		{name: "an absent imap_pass takes the stored one", storedPass: "stored", edit: keep, want: api.ValidationResult{Valid: true, Errors: []string{}}},
		{name: "an absent imap_pass with none stored", edit: keep, want: api.ValidationResult{Errors: []string{"email.imap_pass is required"}}},
		{name: "a posted imap_pass", edit: func(c *api.Config) { c.Email.ImapPass = new("posted") }, want: api.ValidationResult{Valid: true, Errors: []string{}}},
		{name: "an empty posted imap_pass replaces the stored one", storedPass: "stored", edit: func(c *api.Config) { c.Email.ImapPass = new("") },
			want: api.ValidationResult{Errors: []string{"email.imap_pass is required"}}},
		{name: "a posted imap_pass_set is ignored", edit: func(c *api.Config) { c.Email.ImapPassSet = new(true) }, want: api.ValidationResult{Errors: []string{"email.imap_pass is required"}}},
		{name: "one message for each invalid value", storedPass: "stored", edit: func(c *api.Config) { c.Account.PasswordLength, c.Database = 2, "" },
			want: api.ValidationResult{Errors: []string{"database must not be empty", "account.password_length must be at least 8, got 2"}}},
		{name: "a database that is not the database of serve", storedPass: "stored", edit: func(c *api.Config) { c.Database = "other.db" },
			want: api.ValidationResult{Errors: []string{`database must be the database of serve, "creator.db", got "other.db"`}}},
		{name: "a spelling of the database of serve that Clean makes the same", storedPass: "stored", edit: func(c *api.Config) { c.Database = "./creator.db" },
			want: api.ValidationResult{Valid: true, Errors: []string{}}},
		{name: "an absolute spelling of the database of serve is refused", storedPass: "stored", edit: func(c *api.Config) { c.Database = absDatabase },
			want: api.ValidationResult{Errors: []string{`database must be the database of serve, "creator.db", got ` + strconv.Quote(absDatabase)}}},
		{name: "no default fills a zero value", storedPass: "stored", edit: func(c *api.Config) { c.Concurrency.MaxBrowsers = 0 },
			want: api.ValidationResult{Errors: []string{"concurrency.max_browsers must be at least 1, got 0"}}},
		{name: "oauth2 needs no imap_pass", edit: postedOAuth2, want: api.ValidationResult{Valid: true, Errors: []string{}}},
		{name: "oauth2 with no refresh token stored or posted", edit: func(c *api.Config) {
			postedOAuth2(c)
			c.Email.Oauth2.RefreshToken, c.Email.Oauth2.RefreshTokenSet = nil, new(true)
		}, want: api.ValidationResult{Errors: []string{"email.oauth2.refresh_token is required"}}},
		{name: "an absent auth", storedPass: "stored", edit: func(c *api.Config) { c.Email.Auth = "" },
			want: api.ValidationResult{Errors: []string{`email.auth must be "password" or "oauth2", got ""`}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := storedConfig(t, tt.storedPass)
			w := doAPI(t.Context(), newTestAPI(cfg), "POST", "/api/config/validate", postedConfig(t, cfg, tt.edit))

			var got api.ValidationResult
			decodeBody(t, w, http.StatusOK, &got)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("result = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestValidateConfigBadRequest(t *testing.T) {
	cfg := storedConfig(t, "stored")
	tests := []struct {
		name, body, wantPrefix string
		want                   int
	}{
		{"bad chrome.timeout", postedConfig(t, cfg, func(c *api.Config) { c.Chrome.Timeout = "soon" }), `chrome.timeout: time: invalid duration "soon"`, 400},
		{"bad account_timeout", postedConfig(t, cfg, func(c *api.Config) { c.Concurrency.AccountTimeout = "" }), `concurrency.account_timeout: time: invalid duration ""`, 400},
		{"not JSON", "{", "can't decode JSON body: ", 400},
		{"a body over the bound", `{"database":"` + strings.Repeat("a", maxBodyBytes) + `"}`, fmt.Sprintf("the body is larger than %d bytes", maxBodyBytes), 413},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := doAPI(t.Context(), newTestAPI(cfg), "POST", "/api/config/validate", tt.body)

			var got api.Error
			decodeBody(t, w, tt.want, &got)
			if !strings.HasPrefix(got.Message, tt.wantPrefix) {
				t.Errorf("message = %q, want the prefix %q", got.Message, tt.wantPrefix)
			}
		})
	}
}

func TestCheckChrome(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		t.Setenv("CHROME_PATH", fakeChrome(t))

		var got api.ChromeCheck
		decodeBody(t, doAPI(t.Context(), newTestAPI(storedConfig(t, "")), "GET", "/api/checks/chrome", ""), http.StatusOK, &got)

		if !got.Ok || got.Error != nil || got.Version == nil || *got.Version == "" || got.Mismatch == nil || !*got.Mismatch || got.ShapeMajor != jagexapi.ShapeChromeMajor {
			t.Errorf("result = %+v, want ok, a version, mismatch and shape_major %d", got, jagexapi.ShapeChromeMajor)
		}
	})
	t.Run("version read fails", func(t *testing.T) {
		t.Setenv("CHROME_PATH", junkChrome(t))

		var got api.ChromeCheck
		decodeBody(t, doAPI(t.Context(), newTestAPI(storedConfig(t, "")), "GET", "/api/checks/chrome", ""), http.StatusOK, &got)

		if got.Ok || got.Error == nil || !strings.HasPrefix(*got.Error, "getting chrome version: ") || got.Version != nil || got.Mismatch != nil {
			t.Errorf("result = %+v, want not ok and a version error", got)
		}
	})
}

// A posted mailbox takes the stored secrets of the mailbox with its address, wherever the list has it.
func TestEmailFromAPIMailboxes(t *testing.T) {
	storedBox := func(user, pass, token string) config.MailboxConfig {
		return config.MailboxConfig{IMAPUser: user, IMAPPass: pass, Auth: config.AuthOAuth2,
			OAuth2: config.OAuth2Config{ClientSecret: "secret-" + user, RefreshToken: token}}
	}
	stored := config.EmailConfig{Mailboxes: []config.MailboxConfig{
		storedBox("a@outlook.com", "pass-a", "token-a"),
		storedBox("b@outlook.com", "pass-b", "token-b"),
		storedBox("c@outlook.com", "pass-c", "token-c"),
	}}
	posted := func(user string, token *string) api.MailboxConfig {
		return api.MailboxConfig{ImapUser: user, Auth: api.MailboxConfigAuth(config.AuthOAuth2), Oauth2: api.OAuth2Config{RefreshToken: token}}
	}
	list := api.MailboxConfigList{posted("C@Outlook.com", nil), posted("a@outlook.com", nil), posted("d@outlook.com", new("token-d"))}

	got := emailFromAPI(api.EmailConfig{Mailboxes: &list}, stored).Mailboxes

	want := []config.MailboxConfig{
		{IMAPUser: "C@Outlook.com", IMAPPass: "pass-c", Auth: config.AuthOAuth2, OAuth2: config.OAuth2Config{ClientSecret: "secret-c@outlook.com", RefreshToken: "token-c"}},
		{IMAPUser: "a@outlook.com", IMAPPass: "pass-a", Auth: config.AuthOAuth2, OAuth2: config.OAuth2Config{ClientSecret: "secret-a@outlook.com", RefreshToken: "token-a"}},
		{IMAPUser: "d@outlook.com", Auth: config.AuthOAuth2, OAuth2: config.OAuth2Config{RefreshToken: "token-d"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mailboxes = %+v, want %+v", got, want)
	}
}

func TestCheckMailbox(t *testing.T) {
	const section = `{"imap_host":"posted.example.com","imap_port":143,"imap_user":"posted-user","imap_domains":["posted.example.com","other.example.com"],"auth":"password"`
	loginErr := errors.New("login refused")
	tests := []struct {
		name     string
		body     string
		loginErr error
		wantPass string
		want     api.MailboxCheck
	}{
		{name: "an absent imap_pass takes the stored one", body: section + "}", wantPass: "stored", want: api.MailboxCheck{Ok: true}},
		{name: "a posted imap_pass", body: section + `,"imap_pass":"posted"}`, wantPass: "posted", want: api.MailboxCheck{Ok: true}},
		{name: "a failed login", body: section + "}", loginErr: loginErr, wantPass: "stored", want: api.MailboxCheck{Error: new("login refused")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := storedConfig(t, "stored")
			var gotCfg *config.Config
			s := &apiServer{cfg: cfg, logger: logtest.New(io.Discard), checkLoginFn: func(_ context.Context, c *config.Config) error {
				gotCfg = c
				return tt.loginErr
			}}

			var got api.MailboxCheck
			decodeBody(t, doAPI(t.Context(), s.handler(), "POST", "/api/checks/mailbox", tt.body), http.StatusOK, &got)

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("result = %+v, want %+v", got, tt.want)
			}
			wantEmail := config.EmailConfig{IMAPHost: "posted.example.com", IMAPPort: 143, IMAPUser: "posted-user", IMAPPass: tt.wantPass, IMAPDomains: []string{"posted.example.com", "other.example.com"},
				Auth: config.AuthPassword}
			if gotCfg == nil || !reflect.DeepEqual(gotCfg.Email, wantEmail) || gotCfg.Database != cfg.Database {
				t.Fatalf("the login got %+v, want the stored config with the email section %+v", gotCfg, wantEmail)
			}
			if cfg.Email.IMAPHost != "mail.example.com" {
				t.Errorf("the check changed the stored config: %+v", cfg.Email)
			}
		})
	}
	t.Run("an absent oauth2 secret takes the stored one", func(t *testing.T) {
		var gotCfg *config.Config
		s := &apiServer{cfg: storedOAuth2Config(t), logger: logtest.New(io.Discard), checkLoginFn: func(_ context.Context, c *config.Config) error {
			gotCfg = c
			return nil
		}}
		body := `{"imap_host":"mail.example.com","imap_port":993,"imap_user":"user","imap_domains":["example.com"],"auth":"oauth2",` +
			`"oauth2":{"client_id":"posted-client","refresh_token":"posted-refresh","token_url":"https://oauth2.example.com/token"}}`

		var got api.MailboxCheck
		decodeBody(t, doAPI(t.Context(), s.handler(), "POST", "/api/checks/mailbox", body), http.StatusOK, &got)

		want := config.OAuth2Config{ClientID: "posted-client", ClientSecret: "stored-secret", RefreshToken: "posted-refresh", TokenURL: "https://oauth2.example.com/token"}
		if !got.Ok || gotCfg == nil || gotCfg.Email.Auth != config.AuthOAuth2 || gotCfg.Email.OAuth2 != want {
			t.Errorf("result = %+v, the login got %+v, want ok and the oauth2 section %+v", got, gotCfg, want)
		}
	})
	t.Run("a mailboxes list checks each mailbox in place of the section", func(t *testing.T) {
		var mu sync.Mutex
		var gotEmails []config.EmailConfig
		s := &apiServer{cfg: storedConfig(t, "stored"), logger: logtest.New(io.Discard), checkLoginFn: func(_ context.Context, c *config.Config) error {
			mu.Lock()
			defer mu.Unlock()
			gotEmails = append(gotEmails, c.Email)
			if c.Email.IMAPUser == "b@outlook.com" {
				return loginErr
			}
			return nil
		}}
		mailbox := func(user string) string {
			return `{"imap_host":"outlook.office365.com","imap_port":993,"imap_user":"` + user + `","imap_domains":[],"auth":"oauth2",` +
				`"oauth2":{"client_id":"client","refresh_token":"refresh-` + user + `","token_url":"https://oauth2.example.com/token"}}`
		}
		body := section + `,"mailboxes":[` + mailbox("a@outlook.com") + "," + mailbox("b@outlook.com") + "," + mailbox("c@outlook.com") + "]}"

		var got api.MailboxCheck
		decodeBody(t, doAPI(t.Context(), s.handler(), "POST", "/api/checks/mailbox", body), http.StatusOK, &got)

		want := api.MailboxCheck{Error: new("1 of 3 mailboxes failed the login"), Mailboxes: &[]api.MailboxLogin{
			{ImapUser: "a@outlook.com", Ok: true},
			{ImapUser: "b@outlook.com", Error: new("login refused")},
			{ImapUser: "c@outlook.com", Ok: true},
		}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("result = %+v, want %+v", got, want)
		}
		slices.SortFunc(gotEmails, func(a, b config.EmailConfig) int { return strings.Compare(a.IMAPUser, b.IMAPUser) })
		users := []string{"a@outlook.com", "b@outlook.com", "c@outlook.com"}
		wantEmails := make([]config.EmailConfig, 0, len(users))
		for _, user := range users {
			wantEmails = append(wantEmails, config.EmailConfig{IMAPHost: "outlook.office365.com", IMAPPort: 993, IMAPUser: user, IMAPDomains: []string{}, Auth: config.AuthOAuth2,
				OAuth2: config.OAuth2Config{ClientID: "client", RefreshToken: "refresh-" + user, TokenURL: "https://oauth2.example.com/token"}})
		}
		if !reflect.DeepEqual(gotEmails, wantEmails) {
			t.Errorf("the logins got %+v, want one email section for each mailbox, %+v", gotEmails, wantEmails)
		}
	})
	t.Run("a mailboxes list whose every login succeeds", func(t *testing.T) {
		s := &apiServer{cfg: storedConfig(t, "stored"), logger: logtest.New(io.Discard), checkLoginFn: func(context.Context, *config.Config) error { return nil }}
		body := section + `,"mailboxes":[{"imap_host":"h","imap_port":993,"imap_user":"a@outlook.com","imap_domains":[],"auth":"password","oauth2":{"client_id":"","token_url":""}}]}`

		var got api.MailboxCheck
		decodeBody(t, doAPI(t.Context(), s.handler(), "POST", "/api/checks/mailbox", body), http.StatusOK, &got)

		if want := (api.MailboxCheck{Ok: true, Mailboxes: &[]api.MailboxLogin{{ImapUser: "a@outlook.com", Ok: true}}}); !reflect.DeepEqual(got, want) {
			t.Errorf("result = %+v, want %+v", got, want)
		}
	})
	t.Run("the real login on an ended request", func(t *testing.T) {
		ended, cancel := context.WithCancel(t.Context())
		cancel()

		var got api.MailboxCheck
		decodeBody(t, doAPI(ended, newTestAPI(storedConfig(t, "stored")), "POST", "/api/checks/mailbox", section+"}"), http.StatusOK, &got)

		if got.Ok || got.Error == nil || !strings.HasPrefix(*got.Error, "connecting to IMAP: ") {
			t.Errorf("result = %+v, want not ok and a connecting to IMAP error", got)
		}
	})
}

// failingWriter is a response writer whose client has gone.
type failingWriter struct{ *httptest.ResponseRecorder }

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("client gone") }

func TestRequestErrorWriteFails(t *testing.T) {
	var buf bytes.Buffer
	s := &apiServer{logger: logtest.New(&buf)}

	s.requestError(failingWriter{httptest.NewRecorder()}, httptest.NewRequest("POST", "/api/config/validate", http.NoBody), errors.New("bad body"))

	const want = `level=DEBUG msg="writing the API error" error="client gone"` + "\n"
	if buf.String() != want {
		t.Errorf("log = %q, want %q", buf.String(), want)
	}
}

func TestErrorMessages(t *testing.T) {
	if got := errorMessages(errors.New("one")); !slices.Equal(got, []string{"one"}) {
		t.Errorf("errorMessages(a plain error) = %q, want [one]", got)
	}
}

// fillConfig sets each field of v to a value that is not zero.
func fillConfig(v reflect.Value) {
	for i := range v.NumField() {
		f := v.Field(i)
		switch {
		case f.Kind() == reflect.Struct:
			fillConfig(f)
		case f.Kind() == reflect.Pointer:
			f.Set(reflect.New(f.Type().Elem()))
			f.Elem().SetBool(true)
		case f.Type() == reflect.TypeFor[time.Duration]():
			f.SetInt(int64(time.Duration(i+1) * time.Second))
		case f.Kind() == reflect.Int:
			f.SetInt(int64(i + 1))
		case f.Kind() == reflect.Bool:
			f.SetBool(true)
		case f.Kind() == reflect.Slice, f.Kind() == reflect.Array:
			elType := f.Type().Elem()
			switch elType.Kind() {
			case reflect.String:
				slice := reflect.MakeSlice(f.Type(), 1, 1)
				slice.Index(0).SetString(v.Type().Field(i).Name)
				f.Set(slice)
			default:
				slice := reflect.MakeSlice(f.Type(), 1, 1)
				zero := reflect.New(elType).Elem()
				switch zero.Kind() {
				case reflect.Pointer:
					zero.Set(reflect.New(elType.Elem()))
				case reflect.Struct:
					fillConfig(zero)
				default:
					zero.Set(reflect.Zero(elType))
				}
				slice.Index(0).Set(zero)
				f.Set(slice)
			}
		default:
			f.SetString(v.Type().Field(i).Name)
		}
	}
}

// The text is the check, because a nil and an empty list are equal as values but encode differently.
func TestDefaultsAPIConfigDomainsEncodeAsArray(t *testing.T) {
	text, err := json.Marshal(apiConfig(config.Defaults()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), `"imap_domains":[]`) {
		t.Errorf("JSON of the defaults = %s, want it to hold \"imap_domains\":[]", text)
	}
}

// A field that the two mapping functions drop comes back as its zero value.
func TestAPIConfigRoundTrip(t *testing.T) {
	var cfg config.Config
	fillConfig(reflect.ValueOf(&cfg).Elem())

	got, err := configFromAPI(apiConfig(&cfg), cfg.Email)

	if err != nil || !reflect.DeepEqual(got, &cfg) {
		t.Errorf("configFromAPI(apiConfig(cfg)) = %+v, %v, want %+v", got, err, cfg)
	}
}

// tagPaths returns the dotted key path of each leaf field of t, from the tag key.
func tagPaths(t reflect.Type, key, prefix string) map[string]bool {
	paths := map[string]bool{}
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get(key), ",")
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			maps.Copy(paths, tagPaths(ft, key, prefix+name+"."))
			continue
		}
		paths[prefix+name] = true
	}
	return paths
}

// The config file and the API Config spell one key set in two homes. This
// detector fails on a key in one home and not in the other.
func TestAPIConfigMatchesConfig(t *testing.T) {
	// The API holds the set flags in addition, in place of the stored secrets.
	apiOnly := map[string]bool{"email.imap_pass_set": true, "email.oauth2.client_secret_set": true, "email.oauth2.refresh_token_set": true}
	yamlKeys := tagPaths(reflect.TypeFor[config.Config](), "yaml", "")
	jsonKeys := tagPaths(reflect.TypeFor[api.Config](), "json", "")
	for _, k := range slices.Sorted(maps.Keys(yamlKeys)) {
		if !jsonKeys[k] {
			t.Errorf("the config key %s has no key in the API Config of internal/api/openapi.yaml", k)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(jsonKeys)) {
		if !yamlKeys[k] && !apiOnly[k] {
			t.Errorf("the API Config key %s has no key in config.Config", k)
		}
	}
	for k := range apiOnly {
		if !jsonKeys[k] {
			t.Errorf("the API Config has no %s key", k)
		}
	}
}

// savedFile is the config file of savingAPI.
const savedFile = "# the mailbox\nemail:\n  imap_host: mail.example.com\n  imap_user: user\n  imap_pass: stored\n  imap_domains:\n    - example.com\n"

// savingAPI returns an apiServer whose config file is config.local.yaml in a
// new working dir. The file holds savedFile, and the server logs to buf.
func savingAPI(t *testing.T, buf *bytes.Buffer) *apiServer {
	t.Helper()
	t.Chdir(t.TempDir())
	if err := os.WriteFile("config.local.yaml", []byte(savedFile), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Read("config.local.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return &apiServer{cfg: cfg, cfgPath: "config.local.yaml", logger: logtest.New(buf)}
}

func TestSaveConfig(t *testing.T) {
	tests := []struct {
		name     string
		edit     func(*api.Config)
		wantPass string
	}{
		{name: "an absent imap_pass keeps the stored one", edit: func(c *api.Config) { c.Debug, c.Concurrency.MaxWorkers = true, 3 }, wantPass: "stored"},
		{name: "a posted imap_pass replaces the stored one", edit: func(c *api.Config) { c.Email.ImapPass = new("posted") }, wantPass: "posted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			s := savingAPI(t, &buf)
			posted := apiConfig(s.config())
			tt.edit(&posted)
			// The password comes from the row, so the test does not trust the handler's choice.
			want, err := configFromAPI(posted, config.EmailConfig{IMAPPass: tt.wantPass})
			if err != nil {
				t.Fatal(err)
			}

			w := doAPI(t.Context(), s.handler(), "PUT", "/api/config", postedConfig(t, s.config(), tt.edit))

			var got api.Config
			decodeBody(t, w, http.StatusOK, &got)
			if strings.Contains(w.Body.String(), tt.wantPass) || !reflect.DeepEqual(got, apiConfig(want)) {
				t.Errorf("body = %s, want %+v with no IMAP password", w.Body.String(), apiConfig(want))
			}
			fromFile, err := config.Read("config.local.yaml")
			if err != nil || !reflect.DeepEqual(fromFile, want) || !reflect.DeepEqual(s.config(), want) {
				t.Errorf("the file holds %+v, %v and serve holds %+v, want %+v in both", fromFile, err, s.config(), want)
			}
			if data, err := os.ReadFile("config.local.yaml"); err != nil || !strings.HasPrefix(string(data), "# the mailbox\nemail:\n") {
				t.Errorf("the file = %q, %v, want its comment to stay", data, err)
			}
			const wantLog = `level=INFO msg="config saved" file=config.local.yaml` + "\n"
			if buf.String() != wantLog {
				t.Errorf("log = %q, want %q", buf.String(), wantLog)
			}
		})
	}
}

// A refused save changes neither the file nor the config of serve.
func TestSaveConfigRefused(t *testing.T) {
	tests := []struct {
		name     string
		edit     func(*api.Config)
		want     int
		wantBody string
	}{
		{name: "an invalid value", edit: func(c *api.Config) { c.Account.PasswordLength = 2 }, want: http.StatusUnprocessableEntity,
			wantBody: `{"errors":["account.password_length must be at least 8, got 2"],"valid":false}`},
		{name: "a zero timeout", edit: func(c *api.Config) { c.Chrome.Timeout = "0s" }, want: http.StatusUnprocessableEntity,
			wantBody: `{"errors":["chrome.timeout must be positive, got 0s"],"valid":false}`},
		{name: "a value that does not parse", edit: func(c *api.Config) { c.Chrome.Timeout = "soon" }, want: http.StatusBadRequest,
			wantBody: `{"message":"chrome.timeout: time: invalid duration \"soon\""}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			s := savingAPI(t, &buf)
			stored := s.config()

			w := doAPI(t.Context(), s.handler(), "PUT", "/api/config", postedConfig(t, stored, tt.edit))

			if w.Code != tt.want || strings.TrimSpace(w.Body.String()) != tt.wantBody {
				t.Errorf("answer = %d %s, want %d %s", w.Code, w.Body.String(), tt.want, tt.wantBody)
			}
			assertUnchanged(t, s, stored)
			if buf.Len() != 0 {
				t.Errorf("log = %q, want none", buf.String())
			}
		})
	}
}

// assertUnchanged fails the test when the config file of s does not hold
// savedFile or the config of s is not stored.
func assertUnchanged(t *testing.T, s *apiServer, stored *config.Config) {
	t.Helper()
	if data, err := os.ReadFile("config.local.yaml"); err != nil || string(data) != savedFile {
		t.Errorf("the file = %q, %v, want it unchanged", data, err)
	}
	if s.config() != stored {
		t.Errorf("the config of serve = %+v, want it unchanged", s.config())
	}
}

func TestSaveConfigWriteFails(t *testing.T) {
	var buf bytes.Buffer
	s := savingAPI(t, &buf)
	stored := s.config()
	s.cfgPath = filepath.Join("missing", "config.local.yaml")

	w := doAPI(t.Context(), s.handler(), "PUT", "/api/config", postedConfig(t, stored, func(*api.Config) {}))

	var got api.Error
	decodeBody(t, w, http.StatusInternalServerError, &got)
	if !strings.HasPrefix(got.Message, "creating the temp config file: ") {
		t.Errorf("message = %q, want a creating the temp config file error", got.Message)
	}
	assertUnchanged(t, s, stored)
	const wantLog = `level=ERROR msg="answering the API request" path=/api/config error="creating the temp config file: `
	if !strings.HasPrefix(buf.String(), wantLog) || strings.Count(buf.String(), "\n") != 1 {
		t.Errorf("log = %q, want one line with the prefix %q", buf.String(), wantLog)
	}
}

// Saves and reads at the same time: the race detector checks the lock, and
// the last save leaves the file and the config of serve equal.
func TestSaveConfigConcurrent(t *testing.T) {
	var buf bytes.Buffer
	s := savingAPI(t, &buf)
	h := s.handler()
	var wg sync.WaitGroup
	for i := range 4 {
		body := postedConfig(t, s.config(), func(c *api.Config) { c.Concurrency.MaxWorkers, c.Concurrency.MaxBrowsers = i+1, i+1 })
		wg.Go(func() {
			if w := doAPI(t.Context(), h, "PUT", "/api/config", body); w.Code != http.StatusOK {
				t.Errorf("PUT = %d %s, want 200", w.Code, w.Body.String())
			}
		})
		wg.Go(func() {
			if w := doAPI(t.Context(), h, "GET", "/api/config", ""); w.Code != http.StatusOK {
				t.Errorf("GET = %d %s, want 200", w.Code, w.Body.String())
			}
		})
	}
	waittest.Wait(t, &wg)

	fromFile, err := config.Read("config.local.yaml")
	if err != nil || !reflect.DeepEqual(fromFile, s.config()) {
		t.Errorf("the file holds %+v, %v, want the config of serve %+v", fromFile, err, s.config())
	}
	if left, err := filepath.Glob("*.tmp"); err != nil || len(left) > 0 {
		t.Errorf("temp files left = %q, %v, want none", left, err)
	}
}
