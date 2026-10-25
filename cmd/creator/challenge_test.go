package main

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/mafredri/cdp"
	"github.com/mafredri/cdp/protocol/runtime"

	"jagex_account_creator/internal/browser/cdptest"
	"jagex_account_creator/internal/logtest"
)

func TestHoldBrowserOpen(t *testing.T) {
	t.Run("returns after the duration", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			holdBrowserOpen(t.Context(), time.Minute)
			if elapsed := time.Since(start); elapsed != time.Minute {
				t.Errorf("returned after %v, want exactly %v", elapsed, time.Minute)
			}
		})
	})

	t.Run("returns when the context ends", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			start := time.Now()
			holdBrowserOpen(ctx, time.Minute)
			if elapsed := time.Since(start); elapsed != 0 {
				t.Errorf("returned after %v, want 0", elapsed)
			}
		})
	})
}

func TestLogPageRequests(t *testing.T) {
	const entries = `[{"name":"https://account.jagex.com/en-GB/manage","type":"fetch"}]`
	requests := `level=DEBUG msg="page network requests" data=` + strconv.Quote(entries) + "\n"
	tests := []struct {
		name        string
		settled     bool  // the settle check answers true
		entriesErr  error // the error of the performance entries read
		wantElapsed time.Duration
		wantLog     string
	}{
		{name: "settled", settled: true, wantLog: requests},
		{
			name:        "a settle timeout warns and still reads the entries",
			wantElapsed: 5 * time.Second,
			wantLog:     `level=WARN msg="waiting for page API calls to settle" error="waiting for the JS condition: context deadline exceeded"` + "\n" + requests,
		},
		{
			name:       "an entries error warns",
			settled:    true,
			entriesErr: errors.New("target closed"),
			wantLog:    `level=WARN msg="failed to get performance entries" error="evaluating JS: target closed"` + "\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rt := &cdptest.Runtime{Answer: func(expr string) (*runtime.EvaluateReply, error) {
					if strings.HasPrefix(expr, "JSON.stringify") {
						if tt.entriesErr != nil {
							return nil, tt.entriesErr
						}
						return cdptest.Value(strconv.Quote(entries)), nil
					}
					return cdptest.Value(strconv.FormatBool(tt.settled)), nil
				}}
				var buf bytes.Buffer
				start := time.Now()

				logPageRequests(t.Context(), &cdp.Client{Runtime: rt}, logtest.New(&buf))

				if elapsed := time.Since(start); elapsed != tt.wantElapsed {
					t.Errorf("logPageRequests() took %v, want exactly %v", elapsed, tt.wantElapsed)
				}
				if got := buf.String(); got != tt.wantLog {
					t.Errorf("log =\n%s\nwant\n%s", got, tt.wantLog)
				}
			})
		})
	}
}
