package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"jagex_account_creator/internal/config"
	"jagex_account_creator/internal/logtest"
)

func TestNewEmailProvider(t *testing.T) {
	t.Run("password", func(t *testing.T) {
		cfg := config.Defaults()
		cfg.Email = config.EmailConfig{IMAPHost: "mail.example.com", IMAPPort: 143, IMAPUser: "user", IMAPPass: "pass", Auth: config.AuthPassword,
			OAuth2: config.OAuth2Config{ClientID: "unused", RefreshToken: "unused", TokenURL: "https://oauth2.example.com/token"}}

		p := newEmailProvider(t.Context(), cfg, logtest.New(io.Discard))

		if p.Host != "mail.example.com" || p.Port != 143 || p.User != "user" || p.Pass != "pass" || p.TokenSource != nil {
			t.Errorf("provider = %+v, want the email section with a password login", p)
		}
	})
	t.Run("oauth2 refreshes with the grant of the config", func(t *testing.T) {
		var form string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			user, secret, _ := r.BasicAuth()
			form = user + " " + secret + " " + r.PostForm.Encode()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"access","token_type":"Bearer","expires_in":3600}`)
		}))
		defer srv.Close()
		cfg := config.Defaults()
		cfg.Email = config.EmailConfig{IMAPHost: "mail.example.com", IMAPUser: "user", IMAPPass: "unused", Auth: config.AuthOAuth2,
			OAuth2: config.OAuth2Config{ClientID: "client", ClientSecret: "secret", RefreshToken: "refresh", TokenURL: srv.URL}}

		p := newEmailProvider(t.Context(), cfg, logtest.New(io.Discard))
		if p.Pass != "" || p.TokenSource == nil {
			t.Fatalf("provider = %+v, want no password and a token source", p)
		}
		tok, err := p.TokenSource.Token()

		if err != nil || tok.AccessToken != "access" {
			t.Fatalf("Token() = %+v, %v, want the access token access", tok, err)
		}
		if want := "client secret grant_type=refresh_token&refresh_token=refresh"; form != want {
			t.Errorf("the token request = %q, want %q", form, want)
		}
	})
}
