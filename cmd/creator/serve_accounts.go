package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"jagex_account_creator/internal/account"
	"jagex_account_creator/internal/api"
	"jagex_account_creator/internal/store"
)

// ListAccounts returns a page of the accounts that the filters select, newest
// first, with the count of all of them. The page holds no password, no TOTP
// secret and no backup codes.
func (s *apiServer) ListAccounts(ctx context.Context, r api.ListAccountsRequestObject) (api.ListAccountsResponseObject, error) {
	before, limit, bad := pageArgs(r.Params.Before, r.Params.Limit)
	var f store.AccountFilter
	if bad == nil {
		f, bad = accountFilter(&r.Params)
	}
	if bad != nil {
		return api.ListAccounts400JSONResponse{BadParameterJSONResponse: *bad}, nil
	}
	got, err := s.store.Accounts(ctx, f, before, limit)
	if err != nil {
		return nil, err
	}
	page := api.AccountPage{Accounts: make([]api.AccountSummary, len(got.Accounts)), NextBefore: nextBefore(got.Next), Total: got.Total}
	for i := range got.Accounts {
		page.Accounts[i] = apiAccountSummary(&got.Accounts[i])
	}
	return api.ListAccounts200JSONResponse(page), nil
}

// GetAccountByID returns one account with its secrets and, with a TOTP secret,
// the TOTP code of the server clock.
func (s *apiServer) GetAccountByID(ctx context.Context, r api.GetAccountByIDRequestObject) (api.GetAccountByIDResponseObject, error) {
	a, err := s.store.Account(ctx, r.Id)
	if errors.Is(err, store.ErrNotFound) {
		return api.GetAccountByID404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	out := apiAccount(&a)
	if a.Account.TOTPSecret != "" {
		// A secret that does not decode leaves the code absent. The answer still shows the secret and the password.
		if code, left, err := account.CurrentTOTP(a.Account.TOTPSecret, time.Now()); err == nil {
			out.TotpCode, out.TotpRemainingSeconds = &code, &left
		}
	}
	return api.GetAccountByID200JSONResponse(out), nil
}

// LookupAccounts returns the id of the saved account of each posted email that has one, in the order of the emails.
func (s *apiServer) LookupAccounts(ctx context.Context, r api.LookupAccountsRequestObject) (api.LookupAccountsResponseObject, error) {
	matches := []api.AccountMatch{}
	for _, email := range r.Body.Emails {
		a, err := s.store.AccountByEmail(ctx, email)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		matches = append(matches, api.AccountMatch{Email: email, Id: a.ID})
	}
	return api.LookupAccounts200JSONResponse{Accounts: matches}, nil
}

// ConfirmAccount marks one account as checked by hand and returns it with no
// secret. A second call changes nothing.
func (s *apiServer) ConfirmAccount(ctx context.Context, r api.ConfirmAccountRequestObject) (api.ConfirmAccountResponseObject, error) {
	err := s.store.ConfirmAccount(ctx, r.Id)
	var a store.StoredAccount
	if err == nil {
		s.logger.Info("account confirmed", "id", r.Id)
		// No code deletes an account, so the read after the write needs no transaction.
		a, err = s.store.Account(ctx, r.Id)
	}
	if errors.Is(err, store.ErrNotFound) {
		return api.ConfirmAccount404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return api.ConfirmAccount200JSONResponse(apiAccountSummary(&a)), nil
}

// ExportAccounts returns the file of the accounts that the filters select, in
// the format of the request.
func (s *apiServer) ExportAccounts(ctx context.Context, r api.ExportAccountsRequestObject) (api.ExportAccountsResponseObject, error) {
	p := r.Params
	var export func(context.Context, io.Writer, store.AccountFilter) error
	var ext string
	switch p.Format {
	case api.ExportFormatJSONL:
		export, ext = s.store.ExportJSONL, "jsonl"
	case api.ExportFormatCSV:
		export, ext = s.store.ExportCSV, "csv"
	case api.ExportFormatUserPass:
		export, ext = s.store.ExportUserPass, "txt"
	default:
		// The generated code does not check the enum.
		bad := api.BadParameterJSONResponse{Message: fmt.Sprintf("format must be jsonl, csv or userpass, got %q", p.Format)}
		return api.ExportAccounts400JSONResponse{BadParameterJSONResponse: bad}, nil
	}
	f, bad := accountFilter(&api.ListAccountsParams{
		Run: p.Run, Unconfirmed: p.Unconfirmed, Totp: p.Totp, Proxy: p.Proxy,
		CreatedFrom: p.CreatedFrom, CreatedTo: p.CreatedTo, Q: p.Q,
	})
	if bad != nil {
		return api.ExportAccounts400JSONResponse{BadParameterJSONResponse: *bad}, nil
	}
	// The whole file is read first, so a store error answers 500 and never sends part of a file.
	var buf bytes.Buffer
	if err := export(ctx, &buf, f); err != nil {
		return nil, err
	}
	headers := api.ExportAccounts200ResponseHeaders{ContentDisposition: `attachment; filename="accounts.` + ext + `"`}
	switch p.Format {
	case api.ExportFormatCSV:
		return api.ExportAccounts200TextcsvResponse{Body: &buf, Headers: headers, ContentLength: int64(buf.Len())}, nil
	case api.ExportFormatUserPass:
		return api.ExportAccounts200TextResponse{Body: buf.String(), Headers: headers}, nil
	default:
		return api.ExportAccounts200ApplicationxNdjsonResponse{Body: &buf, Headers: headers, ContentLength: int64(buf.Len())}, nil
	}
}

// accountFilter returns the store filter of the filter parameters of p. A run
// below 1 gets the answer bad, which names it.
func accountFilter(p *api.ListAccountsParams) (f store.AccountFilter, bad *api.BadParameterJSONResponse) {
	f = store.AccountFilter{Unconfirmed: p.Unconfirmed, HasTOTP: p.Totp}
	if p.Run != nil {
		if *p.Run < 1 {
			return f, &api.BadParameterJSONResponse{Message: fmt.Sprintf("run must be at least 1, got %d", *p.Run)}
		}
		f.RunID = *p.Run
	}
	if p.Proxy != nil {
		f.Proxy = *p.Proxy
	}
	if p.CreatedFrom != nil {
		f.CreatedFrom = *p.CreatedFrom
	}
	if p.CreatedTo != nil {
		f.CreatedTo = *p.CreatedTo
	}
	if p.Q != nil {
		f.Query = *p.Q
	}
	return f, nil
}

// apiAccountSummary returns the API form of a with no secret. A field that the
// JSONL record omits is absent.
func apiAccountSummary(a *store.StoredAccount) api.AccountSummary {
	acct := &a.Account
	return api.AccountSummary{
		Id:                   a.ID,
		RunId:                optional(a.RunID),
		Email:                acct.Email,
		Username:             acct.Username,
		CreatedAt:            acct.CreatedAt,
		Totp:                 acct.TOTPSecret != "",
		Proxy:                optional(acct.Proxy),
		BrowserBytesSent:     optional(acct.BrowserBytesSent),
		BrowserBytesReceived: optional(acct.BrowserBytesReceived),
		HttpBytesSent:        optional(acct.HTTPBytesSent),
		HttpBytesReceived:    optional(acct.HTTPBytesReceived),
		Unconfirmed:          optional(acct.Unconfirmed),
	}
}

// apiAccount returns the API form of a with its secrets.
func apiAccount(a *store.StoredAccount) api.Account {
	sum := apiAccountSummary(a)
	out := api.Account{
		Id:                   sum.Id,
		RunId:                sum.RunId,
		Email:                sum.Email,
		Username:             sum.Username,
		Password:             a.Account.Password,
		CreatedAt:            sum.CreatedAt,
		Totp:                 sum.Totp,
		TotpSecret:           optional(a.Account.TOTPSecret),
		Proxy:                sum.Proxy,
		BrowserBytesSent:     sum.BrowserBytesSent,
		BrowserBytesReceived: sum.BrowserBytesReceived,
		HttpBytesSent:        sum.HttpBytesSent,
		HttpBytesReceived:    sum.HttpBytesReceived,
		Unconfirmed:          sum.Unconfirmed,
	}
	if len(a.Account.BackupCodes) > 0 {
		out.BackupCodes = new(a.Account.BackupCodes)
	}
	return out
}
