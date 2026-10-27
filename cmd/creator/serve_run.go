package main

import (
	"context"
	"errors"
	"log/slog"

	"jagex_account_creator/internal/api"
)

// GetRun returns the state of the current or last run.
func (s *apiServer) GetRun(context.Context, api.GetRunRequestObject) (api.GetRunResponseObject, error) {
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	return api.GetRun200JSONResponse(state.snapshot()), nil
}

// StartRun validates the posted config and starts a run with it. Only a stop
// stage ends the run, not the end of the request. The run saves its row to the
// store of serve, and the state gets the id of that row. The run ends its state
// with its error before its runControl reports the end.
func (s *apiServer) StartRun(ctx context.Context, r api.StartRunRequestObject) (api.StartRunResponseObject, error) {
	cfg, result, err := validatePosted(*r.Body, s.config())
	if err != nil {
		return api.StartRun400JSONResponse{Message: err.Error()}, nil
	}
	if !result.Valid {
		return api.StartRun422JSONResponse(result), nil
	}
	// The run checks again at its start. This check answers the start before a run row exists.
	if _, _, _, err := mailboxPlan(ctx, s.store, cfg); errors.Is(err, errTooFewMailboxes) {
		return api.StartRun422JSONResponse{Errors: []string{err.Error()}}, nil
	} else if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return api.StartRun409JSONResponse{Message: "serve is stopping"}, nil
	case s.current != nil && !ended(s.current):
		return api.StartRun409JSONResponse{Message: "a run is in progress"}, nil
	}
	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}
	// Limitation: a debug: true start leaves serve's own request lines at DEBUG until the next start sets the level again.
	s.level.Set(level)
	// The first publish, the run start and the swap stay under s.mu, so a stream that subscribes under s.mu sees the new run.
	state := newRunState(cfg.Concurrency.AccountCount, s.events)
	logOpts := runLogOptions{base: s.logger.Handler(), level: s.level, logs: s.logs}
	s.current = startRun(context.WithoutCancel(ctx), func(ctx context.Context, stopNow <-chan struct{}) error {
		err := recordRun(ctx, s.store, api.RunSourceServe, cfg, logOpts, func(ctx context.Context, logger *slog.Logger, h runHistory) error {
			state.runRecorded(h.runID)
			return s.runFn(ctx, stopNow, logger, cfg, state, h)
		})
		state.end(err)
		return err
	})
	s.state = state
	return api.StartRun202JSONResponse(state.snapshot()), nil
}

// StopRun calls the first stop stage of the run in progress.
func (s *apiServer) StopRun(context.Context, api.StopRunRequestObject) (api.StopRunResponseObject, error) {
	return api.StopRun200JSONResponse(s.stop(false)), nil
}

// StopRunNow calls the second stop stage of the run in progress.
func (s *apiServer) StopRunNow(context.Context, api.StopRunNowRequestObject) (api.StopRunNowResponseObject, error) {
	return api.StopRunNow200JSONResponse(s.stop(true)), nil
}

// stop calls the stop stage of the current run, the second stage when now is
// true, and returns the state. A run that has ended keeps its state.
func (s *apiServer) stop(now bool) api.RunState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil {
		if now {
			s.current.StopNow()
		} else {
			s.current.Stop()
		}
		s.state.stop(now)
	}
	return s.state.snapshot()
}

// drainRun refuses new starts, calls the first stop stage of the current run
// and waits for its end. A close of stopNow during the wait calls the second stage.
func (s *apiServer) drainRun(stopNow <-chan struct{}) {
	s.mu.Lock()
	s.closed = true
	c := s.current
	s.mu.Unlock()
	if c == nil {
		return
	}
	s.stop(false)
	select {
	case <-c.done:
		return
	case <-stopNow:
	}
	s.stop(true)
	<-c.done
}

// ended reports whether the run of c has returned.
func ended(c *runControl) bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}
