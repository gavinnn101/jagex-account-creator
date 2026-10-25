package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"jagex_account_creator/internal/api"
	"jagex_account_creator/internal/store"
)

// The bounds of the limit of a page, as internal/api/openapi.yaml declares them. The generated code checks neither.
const (
	defaultPageLimit = 100
	maxPageLimit     = 500
)

// ListRuns returns a page of the runs, newest first, each with its counts.
func (s *apiServer) ListRuns(ctx context.Context, r api.ListRunsRequestObject) (api.ListRunsResponseObject, error) {
	before, limit, bad := pageArgs(r.Params.Before, r.Params.Limit)
	if bad != nil {
		return api.ListRuns400JSONResponse{BadParameterJSONResponse: *bad}, nil
	}
	runs, next, err := s.store.Runs(ctx, before, limit)
	var page []api.Run
	if err == nil {
		page, err = s.apiRuns(ctx, runs)
	}
	if err != nil {
		return nil, err
	}
	return api.ListRuns200JSONResponse{Runs: page, NextBefore: nextBefore(next)}, nil
}

// GetRunByID returns one run with its counts.
func (s *apiServer) GetRunByID(ctx context.Context, r api.GetRunByIDRequestObject) (api.GetRunByIDResponseObject, error) {
	run, err := s.store.Run(ctx, r.Id)
	if errors.Is(err, store.ErrNotFound) {
		return api.GetRunByID404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	var runs []api.Run
	if err == nil {
		runs, err = s.apiRuns(ctx, []store.Run{run})
	}
	if err != nil {
		return nil, err
	}
	return api.GetRunByID200JSONResponse(runs[0]), nil
}

// ListRunAttempts returns a page of the attempts of one run, newest first.
func (s *apiServer) ListRunAttempts(ctx context.Context, r api.ListRunAttemptsRequestObject) (api.ListRunAttemptsResponseObject, error) {
	before, limit, bad := pageArgs(r.Params.Before, r.Params.Limit)
	if bad != nil {
		return api.ListRunAttempts400JSONResponse{BadParameterJSONResponse: *bad}, nil
	}
	attempts, next, err := s.store.Attempts(ctx, r.Id, before, limit)
	if err == nil && len(attempts) == 0 {
		// An empty page is also the page of a run that does not exist, so only it reads the run.
		_, err = s.store.Run(ctx, r.Id)
	}
	if errors.Is(err, store.ErrNotFound) {
		return api.ListRunAttempts404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	page := api.AttemptPage{Attempts: make([]api.Attempt, len(attempts)), NextBefore: nextBefore(next)}
	for i := range attempts {
		page.Attempts[i] = apiAttempt(&attempts[i])
	}
	return api.ListRunAttempts200JSONResponse(page), nil
}

// GetRunLog sends the log file of one run. A run that does not exist, or a run
// with no log file, gets 404.
func (s *apiServer) GetRunLog(ctx context.Context, r api.GetRunLogRequestObject) (api.GetRunLogResponseObject, error) {
	_, err := s.store.Run(ctx, r.Id)
	if errors.Is(err, store.ErrNotFound) {
		return api.GetRunLog404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	path := runLogPath(s.config().Database, r.Id)
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return api.GetRunLog404JSONResponse{NotFoundJSONResponse: notFound(fmt.Errorf("run %d has no log file", r.Id))}, nil
	}
	var info fs.FileInfo
	if err == nil {
		info, err = f.Stat()
	}
	if err != nil {
		// Close of a nil *os.File does nothing, so one branch serves a failed open and a failed Stat.
		_ = f.Close()
		return nil, fmt.Errorf("opening the run log: %w", err)
	}
	return runLogDownload{f: f, size: info.Size(), name: filepath.Base(path)}, nil
}

// runLogDownload is the answer of a run log download: the first size bytes of
// f, as the file name. A run in progress still appends to f, so the copy stops
// at the size of its Content-Length.
type runLogDownload struct {
	f    *os.File
	size int64
	name string
}

// VisitGetRunLogResponse sends the file and closes it. Each write moves the
// write deadline, so a large file outlives the server's write timeout. A copy
// that stops short ends the connection before Content-Length, so the client
// sees a failed download.
func (d runLogDownload) VisitGetRunLogResponse(w http.ResponseWriter) error {
	defer func() { _ = d.f.Close() }()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.FormatInt(d.size, 10))
	w.Header().Set("Content-Disposition", `attachment; filename="`+d.name+`"`)
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(deadlineWriter{w, http.NewResponseController(w)}, io.LimitReader(d.f, d.size)); err != nil {
		return fmt.Errorf("sending the run log: %w", err)
	}
	return nil
}

// pageArgs returns the before and the limit of a page request. An absent
// before is the first page, and an absent limit is defaultPageLimit. A value
// out of range gets the answer bad, which names it.
func pageArgs(before *int64, limit *int) (b int64, l int, bad *api.BadParameterJSONResponse) {
	l = defaultPageLimit
	if before != nil {
		if *before < 1 {
			return 0, 0, &api.BadParameterJSONResponse{Message: fmt.Sprintf("before must be at least 1, got %d", *before)}
		}
		b = *before
	}
	if limit != nil {
		if *limit < 1 || *limit > maxPageLimit {
			return 0, 0, &api.BadParameterJSONResponse{Message: fmt.Sprintf("limit must be from 1 to %d, got %d", maxPageLimit, *limit)}
		}
		l = *limit
	}
	return b, l, nil
}

// notFound returns the 404 answer of err, a read whose row does not exist.
func notFound(err error) api.NotFoundJSONResponse {
	return api.NotFoundJSONResponse{Message: err.Error()}
}

// nextBefore returns the next_before of a page whose store read returned next.
// The next of the last page is 0, and its next_before is absent.
func nextBefore(next int64) *int64 {
	if next == 0 {
		return nil
	}
	return new(next)
}

// apiRuns returns the API form of runs. One CountRuns call reads the counts of
// every run on the page.
func (s *apiServer) apiRuns(ctx context.Context, runs []store.Run) ([]api.Run, error) {
	ids := make([]int64, len(runs))
	for i := range runs {
		ids[i] = runs[i].ID
	}
	counts, err := s.store.CountRuns(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]api.Run, len(runs))
	for i := range runs {
		if out[i], err = apiRun(&runs[i], counts[runs[i].ID]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// apiRun returns the API form of r with the counts c. The config comes from
// the JSON that the run saved.
func apiRun(r *store.Run, c store.RunCounts) (api.Run, error) {
	out := api.Run{
		Id:        r.ID,
		Source:    api.RunSource(r.Source),
		StartedAt: r.StartedAt,
		Error:     optional(r.Error),
		Counts: api.RunCounts{
			Attempts:             c.Attempts,
			Accounts:             c.Accounts,
			Unconfirmed:          c.Unconfirmed,
			Failures:             c.Failures,
			BrowserBytesSent:     c.BrowserBytesSent,
			BrowserBytesReceived: c.BrowserBytesReceived,
			HttpBytesSent:        c.HTTPBytesSent,
			HttpBytesReceived:    c.HTTPBytesReceived,
		},
	}
	if !r.EndedAt.IsZero() {
		out.EndedAt = new(r.EndedAt)
	}
	var err error
	if out.Config, err = decodeConfig(r.Config, "run", r.ID); err != nil {
		return api.Run{}, err
	}
	return out, nil
}

// decodeConfig returns the API form of text, a config that configJSON stored in
// the row id of kind.
// Limitation: a stored config that no longer decodes as api.Config makes each answer that holds its row a 500. It fails loud. After a change of the Config schema, a migration of runs.config and presets.config repairs the rows.
func decodeConfig(text json.RawMessage, kind string, id int64) (api.Config, error) {
	var c api.Config
	if err := json.Unmarshal(text, &c); err != nil {
		return api.Config{}, fmt.Errorf("reading the config of %s %d: %w", kind, id, err)
	}
	return c, nil
}

// apiAttempt returns the API form of a. An empty text field and an absent
// account become absent fields.
func apiAttempt(a *store.Attempt) api.Attempt {
	out := api.Attempt{
		Id:                   a.ID,
		RunId:                a.RunID,
		Worker:               a.Worker,
		StartedAt:            a.StartedAt,
		DurationMs:           a.Duration.Milliseconds(),
		Proxy:                optional(a.Proxy),
		Category:             optional(a.Category),
		Error:                optional(a.Error),
		FailedStep:           optional(a.FailedStep),
		FailedPhase:          optional(a.FailedPhase),
		Steps:                make([]api.AttemptStep, len(a.Steps)),
		BrowserBytesSent:     a.BrowserBytesSent,
		BrowserBytesReceived: a.BrowserBytesReceived,
		HttpBytesSent:        a.HTTPBytesSent,
		HttpBytesReceived:    a.HTTPBytesReceived,
	}
	for i, st := range a.Steps {
		out.Steps[i] = api.AttemptStep{Name: st.Name, DurationMs: st.Duration.Milliseconds()}
	}
	if a.AccountID != 0 {
		out.AccountId, out.Email, out.Username = new(a.AccountID), new(a.Email), new(a.Username)
	}
	return out
}

// optional returns a pointer to v, or nil when v is the zero value, so that the JSON omits it.
func optional[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return new(v)
}
