package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"jagex_account_creator/internal/api"
	"jagex_account_creator/internal/config"
	"jagex_account_creator/internal/store"
)

// presetPass is the IMAP password of the config of serve in the preset tests.
const presetPass = "stored-secret"

// newPresetsAPI returns an apiServer over a new store with a valid config of serve. It logs to w.
func newPresetsAPI(t *testing.T, w *bytes.Buffer) *apiServer {
	t.Helper()
	s := newServeAPI(w)
	s.cfg = storedConfig(t, presetPass)
	s.store = newTestStore(t)
	return s
}

// presetBody returns the body of a save of name with the config of serve, changed by edit.
func presetBody(t *testing.T, s *apiServer, name string, edit func(*api.Config)) string {
	t.Helper()
	c := apiConfig(s.config())
	edit(&c)
	body, err := json.Marshal(api.PresetSave{Name: name, Config: c})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// postedPresetConfig returns the config form of the config of a presetBody save, as the validation reads it.
func postedPresetConfig(t *testing.T, s *apiServer, edit func(*api.Config)) *config.Config {
	t.Helper()
	c := apiConfig(s.config())
	edit(&c)
	cfg, err := configFromAPI(c, s.config().Email)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// storedPresets returns the presets of s as the store holds them.
func storedPresets(t *testing.T, s *store.Store) []store.Preset {
	t.Helper()
	presets, err := s.Presets(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return presets
}

// listPresets returns the list answer of h.
func listPresets(t *testing.T, h http.Handler) api.PresetList {
	t.Helper()
	var got api.PresetList
	decodeBody(t, doAPI(t.Context(), h, "GET", "/api/presets", ""), http.StatusOK, &got)
	return got
}

func TestListPresets(t *testing.T) {
	s := newPresetsAPI(t, &bytes.Buffer{})
	t.Run("no preset", func(t *testing.T) {
		if w := doAPI(t.Context(), s.handler(), "GET", "/api/presets", ""); w.Code != http.StatusOK || w.Body.String() != "{\"presets\":[]}\n" {
			t.Errorf("answer = %d %s, want 200 with an empty list", w.Code, w.Body.String())
		}
	})
	t.Run("the database and imap_pass_set of serve, in name order", func(t *testing.T) {
		// A row whose database and password state differ from serve's.
		other := *s.config()
		other.Database, other.Email.IMAPPass, other.Concurrency.MaxWorkers = "other.db", "", 7
		for _, name := range []string{"b", "A"} {
			if _, err := s.store.SavePreset(t.Context(), name, configJSON(&other), time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		want := apiConfig(&other)
		want.Database, want.Email.ImapPassSet = "creator.db", new(true)
		stored := storedPresets(t, s.store)
		wantList := api.PresetList{Presets: []api.Preset{
			{Id: 2, Name: "A", SavedAt: stored[0].SavedAt, Config: want},
			{Id: 1, Name: "b", SavedAt: stored[1].SavedAt, Config: want},
		}}
		if g, w := jsonOf(t, listPresets(t, s.handler())), jsonOf(t, wantList); g != w {
			t.Errorf("list =\n%s\nwant\n%s", g, w)
		}
	})
}

func TestSavePresetAPI(t *testing.T) {
	var log bytes.Buffer
	s := newPresetsAPI(t, &log)
	workers := func(n int) func(*api.Config) {
		return func(c *api.Config) { c.Concurrency.MaxWorkers = n }
	}

	w := doAPI(t.Context(), s.handler(), "POST", "/api/presets", presetBody(t, s, "  fast  ", workers(3)))
	var got api.Preset
	decodeBody(t, w, http.StatusOK, &got)
	cfg := postedPresetConfig(t, s, workers(3))
	stored := storedPresets(t, s.store)
	if len(stored) != 1 || string(stored[0].Config) != string(configJSON(cfg)) {
		t.Fatalf("stored = %+v, want one row with the config %s", stored, configJSON(cfg))
	}
	want := api.Preset{Id: 1, Name: "fast", SavedAt: stored[0].SavedAt, Config: apiConfig(cfg)}
	if g, wj := jsonOf(t, got), jsonOf(t, want); g != wj {
		t.Errorf("answer =\n%s\nwant\n%s", g, wj)
	}
	if want := `level=INFO msg="preset saved" id=1 name=fast` + "\n"; log.String() != want {
		t.Errorf("log = %q, want %q", log.String(), want)
	}

	t.Run("a second save under the name keeps the id", func(t *testing.T) {
		posted := func(c *api.Config) {
			c.Concurrency.MaxWorkers = 5
			c.Email.ImapPass = new("posted-secret")
		}
		w := doAPI(t.Context(), s.handler(), "POST", "/api/presets", presetBody(t, s, "fast", posted))
		var got api.Preset
		decodeBody(t, w, http.StatusOK, &got)
		stored := storedPresets(t, s.store)
		wantText := configJSON(postedPresetConfig(t, s, posted))
		if got.Id != 1 || got.Config.Concurrency.MaxWorkers != 5 || len(stored) != 1 || string(stored[0].Config) != string(wantText) {
			t.Errorf("answer = %+v, stored = %+v; want id 1 with 5 workers and the stored config %s", got, stored, wantText)
		}
		for _, secret := range []string{presetPass, "posted-secret"} {
			if strings.Contains(w.Body.String(), secret) || strings.Contains(string(stored[0].Config), secret) {
				t.Errorf("the answer or the stored row holds %s: %s, %s", secret, w.Body.String(), stored[0].Config)
			}
		}
	})
}

// A mailbox of the list takes the set flags of the mailbox of serve with its imap_user, and false with none.
func TestListPresetsMailboxFlags(t *testing.T) {
	s := newPresetsAPI(t, &bytes.Buffer{})
	box := func(user, pass, token string) config.MailboxConfig {
		return config.MailboxConfig{IMAPHost: "outlook.office365.com", IMAPPort: 993, IMAPUser: user, IMAPPass: pass, IMAPDomains: []string{}, Auth: config.AuthOAuth2,
			OAuth2: config.OAuth2Config{ClientID: "client", RefreshToken: token, TokenURL: "https://oauth2.example.com/token"}}
	}
	s.cfg.Email.Mailboxes = []config.MailboxConfig{box("A@outlook.com", "pass", "token")}
	// The row holds the flags of its save: none set for a, and all set for b.
	row := *s.config()
	row.Email.Mailboxes = []config.MailboxConfig{box("a@outlook.com", "", ""), box("b@outlook.com", "pass", "token")}
	if _, err := s.store.SavePreset(t.Context(), "list", configJSON(&row), time.Now()); err != nil {
		t.Fatal(err)
	}

	list := listPresets(t, s.handler())

	if len(list.Presets) != 1 || list.Presets[0].Config.Email.Mailboxes == nil {
		t.Fatalf("list = %+v, want one preset with a mailboxes list", list)
	}
	// The flags of each mailbox: imap_pass_set, client_secret_set and refresh_token_set.
	got := make([][3]bool, 0, len(*list.Presets[0].Config.Email.Mailboxes))
	for _, mb := range *list.Presets[0].Config.Email.Mailboxes {
		got = append(got, [3]bool{*mb.ImapPassSet, *mb.Oauth2.ClientSecretSet, *mb.Oauth2.RefreshTokenSet})
	}
	if want := [][3]bool{{true, false, true}, {false, false, false}}; !slices.Equal(got, want) {
		t.Errorf("flags = %+v, want %+v", got, want)
	}
}

// imap_pass_set describes serve, so a posted password with none in serve answers false, as the list does.
func TestSavePresetAPI_PostedPassWithNoServePass(t *testing.T) {
	s := newPresetsAPI(t, &bytes.Buffer{})
	s.cfg = storedConfig(t, "")
	posted := func(c *api.Config) { c.Email.ImapPass = new("posted-secret") }

	var got api.Preset
	decodeBody(t, doAPI(t.Context(), s.handler(), "POST", "/api/presets", presetBody(t, s, "fast", posted)), http.StatusOK, &got)

	if got.Config.Email.ImapPassSet == nil || *got.Config.Email.ImapPassSet {
		t.Errorf("email of the save answer = %s, want imap_pass_set false", jsonOf(t, got.Config.Email))
	}
	if list := listPresets(t, s.handler()); len(list.Presets) != 1 || jsonOf(t, list.Presets[0]) != jsonOf(t, got) {
		t.Errorf("list = %+v, want one item equal to the save answer %+v", list, got)
	}
}

func TestSavePresetAPI_Refused(t *testing.T) {
	keep := func(*api.Config) {}
	for _, tt := range []struct {
		name, preset string
		edit         func(*api.Config)
		body         string // replaces the body of preset and edit when set
		status       int
		want         string
		result       []string // the errors of a 422
	}{
		{name: "an empty name", preset: "", edit: keep, status: http.StatusBadRequest, want: "name must not be empty"},
		{name: "a blank name", preset: " \t ", edit: keep, status: http.StatusBadRequest, want: "name must not be empty"},
		{name: "a bad duration", preset: "a", edit: func(c *api.Config) { c.Chrome.Timeout = "soon" }, status: http.StatusBadRequest, want: `chrome.timeout: time: invalid duration "soon"`},
		{name: "a body that does not parse", body: "{", status: http.StatusBadRequest, want: "can't decode JSON body: "},
		{name: "an invalid config", preset: "a", edit: func(c *api.Config) { c.Account.PasswordLength = 2 }, status: http.StatusUnprocessableEntity,
			result: []string{"account.password_length must be at least 8, got 2"}},
		{name: "another database", preset: "a", edit: func(c *api.Config) { c.Database = "other.db" }, status: http.StatusUnprocessableEntity,
			result: []string{`database must be the database of serve, "creator.db", got "other.db"`}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var log bytes.Buffer
			s := newPresetsAPI(t, &log)
			body := tt.body
			if body == "" {
				body = presetBody(t, s, tt.preset, tt.edit)
			}
			w := doAPI(t.Context(), s.handler(), "POST", "/api/presets", body)
			if tt.result != nil {
				var got api.ValidationResult
				decodeBody(t, w, tt.status, &got)
				if g, wj := jsonOf(t, got), jsonOf(t, api.ValidationResult{Errors: tt.result}); g != wj {
					t.Errorf("result = %s, want %s", g, wj)
				}
			} else {
				assertAPIError(t, w, tt.status, tt.want)
			}
			if presets := storedPresets(t, s.store); len(presets) != 0 || log.Len() != 0 {
				t.Errorf("stored = %+v, log = %q; want no preset and no log", presets, log.String())
			}
		})
	}
}

func TestDeletePresetAPI(t *testing.T) {
	var log bytes.Buffer
	s := newPresetsAPI(t, &log)
	if w := doAPI(t.Context(), s.handler(), "POST", "/api/presets", presetBody(t, s, "fast", func(*api.Config) {})); w.Code != http.StatusOK {
		t.Fatalf("save = %d %s", w.Code, w.Body.String())
	}
	log.Reset()

	w := doAPI(t.Context(), s.handler(), "DELETE", "/api/presets/1", "")
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("delete = %d %q, want 204 with no body", w.Code, w.Body.String())
	}
	if want := `level=INFO msg="preset deleted" id=1` + "\n"; log.String() != want {
		t.Errorf("log = %q, want %q", log.String(), want)
	}
	if got := listPresets(t, s.handler()); len(got.Presets) != 0 {
		t.Errorf("list after the delete = %+v, want none", got)
	}

	for _, tt := range []struct {
		name, id string
		status   int
		want     string
	}{
		{"a preset that does not exist", "1", http.StatusNotFound, "deleting preset 1: not found"},
		{"an id that is not an integer", "x", http.StatusBadRequest, "Invalid format for parameter id: "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assertAPIError(t, doAPI(t.Context(), s.handler(), "DELETE", "/api/presets/"+tt.id, ""), tt.status, tt.want)
		})
	}
}

// A store error answers 500 with the JSON error and logs one ERROR line.
func TestPresetsStoreErrors(t *testing.T) {
	const closed = "sql: database is closed"
	for _, tt := range []struct {
		name, method, target, body, want string
		badConfig                        bool // the store holds preset 1 with a config that does not decode. Else the store is closed
	}{
		{name: "the list of a closed store", method: "GET", target: "/api/presets", want: "reading presets: " + closed},
		{name: "the save of a closed store", method: "POST", target: "/api/presets", body: "fast", want: `saving preset "fast": ` + closed},
		{name: "the delete of a closed store", method: "DELETE", target: "/api/presets/1", want: "deleting preset 1: " + closed},
		{name: "the list with a stored config that does not decode", method: "GET", target: "/api/presets", badConfig: true,
			want: `reading the config of preset 1: json: cannot unmarshal string into Go struct field Config.debug of type bool`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var log bytes.Buffer
			s := newPresetsAPI(t, &log)
			if tt.badConfig {
				if _, err := s.store.SavePreset(t.Context(), "bad", json.RawMessage(`{"debug":"yes"}`), time.Now()); err != nil {
					t.Fatal(err)
				}
			} else {
				_ = s.store.Close()
			}
			body := ""
			if tt.body != "" {
				body = presetBody(t, s, tt.body, func(*api.Config) {})
			}

			assertAPIError(t, doAPI(t.Context(), s.handler(), tt.method, tt.target, body), http.StatusInternalServerError, tt.want)

			if want := `level=ERROR msg="answering the API request" path=` + tt.target + ` error=` + strconv.Quote(tt.want) + "\n"; log.String() != want {
				t.Errorf("log = %q, want %q", log.String(), want)
			}
		})
	}
}
