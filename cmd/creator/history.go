package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"jagex_account_creator/internal/api"
	"jagex_account_creator/internal/config"
	"jagex_account_creator/internal/store"
)

// runHistory is the account store of a run and the id of its run row. Each
// attempt of the run saves its row there.
type runHistory struct {
	accounts *store.Store
	runID    int64
}

// recordRun saves a new run row of source with the config cfg and opens the log
// file of the run. It runs work with the run logger and the history of that
// row. Then it saves the end of the row with the error of work and closes the
// log file. A failed start save fails the run before work starts. A failed log
// open is the error of the run, and work does not start. A failed end save or
// close joins the error of the run.
func recordRun(ctx context.Context, accounts *store.Store, source api.RunSource, cfg *config.Config, logOpts runLogOptions, work func(context.Context, *slog.Logger, runHistory) error) error {
	// A stop can end ctx at any point of the run, also before the start save. Neither save uses it, so each started run has its row.
	saveCtx := context.WithoutCancel(ctx)
	id, err := accounts.WriteRun(saveCtx, string(source), time.Now(), configJSON(cfg))
	if err != nil {
		return err
	}
	runLog, logger, runErr := openRunLog(cfg.Database, id, logOpts)
	if runErr == nil {
		logger.Info("run started", "run_id", id, "source", source)
		runErr = work(ctx, logger, runHistory{accounts: accounts, runID: id})
		if runErr != nil {
			logger.Info("run ended", "run_id", id, "error", runErr)
		} else {
			logger.Info("run ended", "run_id", id)
		}
	}
	msg := ""
	if runErr != nil {
		msg = runErr.Error()
	}
	err = errors.Join(runErr, accounts.EndRun(saveCtx, id, time.Now(), msg))
	if runLog != nil {
		err = errors.Join(err, runLog.Close())
	}
	return err
}

// saveAttempt saves the row of res to the run history. A failed save logs a
// WARN and changes nothing else, because the worker decides its retry from the
// error of the attempt.
func (a *attempt) saveAttempt(ctx context.Context, res *attemptResult) {
	h := a.run.history
	row := historyAttempt(h.runID, a.worker.worker, res)
	email := ""
	if res.saved() {
		email = res.acct.Email
	}
	// The context of a timed-out attempt has ended, and its row must still reach the store.
	if err := h.accounts.WriteAttempt(context.WithoutCancel(ctx), &row, email); err != nil {
		a.logger.Warn("attempt not saved to the run history", "run_id", h.runID, "worker", a.worker.worker, "error", err)
	}
}

// historyAttempt returns the row of res in the run runID. worker is the number
// of the worker of the attempt. The row holds the values of the record line.
func historyAttempt(runID int64, worker int, res *attemptResult) store.Attempt {
	rec := res.rec
	row := store.Attempt{
		RunID:                runID,
		Worker:               worker,
		StartedAt:            rec.steps.start,
		Duration:             rec.total,
		Proxy:                rec.proxy,
		Category:             res.category,
		Steps:                make([]store.Step, len(rec.steps.steps)),
		BrowserBytesSent:     rec.browserBytesSent,
		BrowserBytesReceived: rec.browserBytesReceived,
		HTTPBytesSent:        rec.httpBytesSent,
		HTTPBytesReceived:    rec.httpBytesReceived,
	}
	for i, s := range rec.steps.steps {
		row.Steps[i] = store.Step{Name: s.name, Duration: s.duration}
	}
	if res.err != nil {
		row.Error = res.err.Error()
	}
	if s, ok := rec.steps.failedStep(); ok {
		row.FailedStep, row.FailedPhase = s.name, s.phase
	}
	return row
}
