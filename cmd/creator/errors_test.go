package main

import (
	"errors"
	"fmt"
	"testing"

	"jagex_account_creator/internal/browser"
	"jagex_account_creator/internal/jagexapi"
)

func TestMarkedError(t *testing.T) {
	tests := []struct {
		name     string
		mark     error
		other    error // the other mark, which must not match
		sentinel error
		err      error // an error that wraps sentinel
	}{
		{name: "cooled proxy error", mark: errProxyCooled, other: errPasswordRefused, sentinel: browser.ErrProxyConnection, err: fmt.Errorf("proxy connectivity check: %w", browser.ErrProxyConnection)},
		{name: "cooled rate limit", mark: errProxyCooled, other: errPasswordRefused, sentinel: jagexapi.ErrRateLimited, err: fmt.Errorf("initial registration: %w: status 429", jagexapi.ErrRateLimited)},
		{name: "refused password", mark: errPasswordRefused, other: errProxyCooled, sentinel: jagexapi.ErrRegistrationFailed, err: fmt.Errorf("submitting password: %w: status 400", jagexapi.ErrRegistrationFailed)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			marked := markedError{err: tt.err, mark: tt.mark}
			if errors.Is(tt.sentinel, tt.mark) || errors.Is(tt.err, tt.mark) {
				t.Errorf("an unmarked error matches %v", tt.mark)
			}
			if !errors.Is(marked, tt.mark) || !errors.Is(fmt.Errorf("outer: %w", marked), tt.mark) {
				t.Errorf("a marked error does not match %v", tt.mark)
			}
			if errors.Is(marked, tt.other) {
				t.Errorf("the marked error matches %v", tt.other)
			}
			if !errors.Is(marked, tt.sentinel) {
				t.Errorf("the marked error does not match %v", tt.sentinel)
			}
			if marked.Error() != tt.err.Error() {
				t.Errorf("text = %q, want %q", marked.Error(), tt.err.Error())
			}
			if got, want := failureCategory(t.Context(), marked), failureCategory(t.Context(), tt.err); got != want {
				t.Errorf("category = %q, want %q", got, want)
			}
		})
	}
}
