package main

import (
	"context"
	"log/slog"

	"golang.org/x/oauth2"

	"jagex_account_creator/internal/config"
	"jagex_account_creator/internal/email"
)

// newEmailProvider builds the IMAP provider of the email section of cfg. With
// auth oauth2 it logs in with XOAUTH2. Its token source asks the token endpoint
// for an access token when it has none or the one it has expires, and ctx bounds
// each refresh.
// Limitation: a token endpoint that rotates the refresh token gives a new one that the config never stores. The old one keeps working where the provider allows that, as Google and Microsoft do.
func newEmailProvider(ctx context.Context, cfg *config.Config, logger *slog.Logger) *email.IMAPProvider {
	e := cfg.Email
	p := &email.IMAPProvider{
		Host:   e.IMAPHost,
		Port:   e.IMAPPort,
		User:   e.IMAPUser,
		Pass:   e.IMAPPass,
		Logger: logger.With("component", "email"),
	}
	if e.Auth == config.AuthOAuth2 {
		o := e.OAuth2
		grant := &oauth2.Config{ClientID: o.ClientID, ClientSecret: o.ClientSecret, Endpoint: oauth2.Endpoint{TokenURL: o.TokenURL}}
		p.Pass, p.TokenSource = "", grant.TokenSource(ctx, &oauth2.Token{RefreshToken: o.RefreshToken})
	}
	return p
}
