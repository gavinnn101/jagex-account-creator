package main

import (
	"context"
	"errors"
	"fmt"

	"jagex_account_creator/internal/account"
	"jagex_account_creator/internal/jagexapi"
)

// totpEnroller is the part of the registration client that 2FA enrollment calls.
type totpEnroller interface {
	Login(ctx context.Context) error
	SetupTOTP(ctx context.Context, email string) (*jagexapi.TOTPSetupResponse, error)
	ActivateTOTP(ctx context.Context, totpID, verificationCode string) ([]string, error)
}

// enrollIfEnabled adds 2FA to acct when the config enables it. A 2FA failure
// only warns, because the account already exists remotely. An activation that
// fails without a refusal keeps the secret, because Jagex can have 2FA active.
// A 429 also cools the proxy.
func (a *attempt) enrollIfEnabled(ctx context.Context, enroller totpEnroller, acct *account.Account) {
	if !*a.run.cfg.Account.EnableTOTP {
		return
	}
	if err := a.enrollTOTP(ctx, enroller, acct); err != nil {
		msg := "2FA enrollment failed, writing the account without it"
		if acct.Unconfirmed {
			msg = "2FA activation unconfirmed, writing the account with its TOTP secret"
		}
		a.logger.Warn(msg, "email", acct.Email, "username", acct.Username, "error", err)
		if errors.Is(err, jagexapi.ErrRateLimited) {
			_ = a.coolProxy("rate limited", err) // The account succeeds, so no worker loop reads the mark.
		}
	}
}

// enrollTOTP logs the account in over HTTP and adds 2FA to it, filling acct's
// TOTP fields on success. Its error never means "account creation failed": the
// caller logs it and still persists the account.
func (a *attempt) enrollTOTP(ctx context.Context, enroller totpEnroller, acct *account.Account) error {
	a.steps.begin("login", phaseHTTP)
	a.logger.Debug("starting 2FA setup")
	if err := enroller.Login(ctx); err != nil {
		return fmt.Errorf("logging in: %w", err)
	}
	return a.activateTOTP(ctx, enroller, acct)
}

// activateTOTP sets up TOTP over HTTP and fills acct's TOTP fields. A refused
// activation leaves the fields empty. Any other failed activation keeps the
// secret, leaves the backup codes empty and marks acct unconfirmed.
func (a *attempt) activateTOTP(ctx context.Context, enroller totpEnroller, acct *account.Account) error {
	// Set up TOTP. The TOTP calls send the management page as their own
	// Referer, so the registration client serves them unchanged.
	a.steps.begin("setup_totp", phaseHTTP)
	totpData, err := enroller.SetupTOTP(ctx, acct.Email)
	if err != nil {
		return err
	}

	// Generate and submit TOTP code.
	a.steps.begin("activate_totp", phaseHTTP)
	totpCode, err := account.GenerateTOTP(ctx, totpData.Secret)
	if err != nil {
		return fmt.Errorf("generating TOTP code: %w", err)
	}

	backupCodes, err := enroller.ActivateTOTP(ctx, totpData.ID, totpCode)
	// Limitation: a 4xx or 429 on the activation POST counts as a refusal. No run checked such an account. It fails toward a lost account.
	if jagexapi.Refused(err) {
		return err
	}
	acct.TOTPSecret = totpData.Secret
	if err != nil {
		// A POST that fails can still reach Jagex, and without the secret the operator cannot log in.
		acct.Unconfirmed = true
		return err
	}
	acct.BackupCodes = backupCodes
	return nil
}
