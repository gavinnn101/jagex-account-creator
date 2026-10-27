package main

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"jagex_account_creator/internal/account"
	"jagex_account_creator/internal/config"
	"jagex_account_creator/internal/waittest"
)

// mailboxes returns a mailbox of the list for each user, with no domain.
func mailboxes(users ...string) []config.MailboxConfig {
	list := make([]config.MailboxConfig, len(users))
	for i, u := range users {
		list[i] = config.MailboxConfig{IMAPHost: "outlook.office365.com", IMAPPort: 993, IMAPUser: u, Auth: config.AuthOAuth2}
	}
	return list
}

// claimAll claims from p until it is empty, at most limit times, and returns the users in claim order.
func claimAll(p *mailboxPool, limit int) []string {
	var users []string
	for range limit {
		src, ok := p.claim()
		if !ok {
			break
		}
		users = append(users, src.IMAPUser)
	}
	return users
}

func TestMailboxPoolClaim(t *testing.T) {
	t.Run("a mailbox with no domain is handed out once", func(t *testing.T) {
		cfg := &config.Config{Email: config.EmailConfig{Mailboxes: mailboxes("a@outlook.com", "b@outlook.com", "c@outlook.com")}}
		p := newMailboxPool(emailSources(cfg))
		if n, ok := p.limit(); n != 3 || !ok {
			t.Errorf("limit = %d, %v, want 3, true", n, ok)
		}
		if got, want := claimAll(p, 10), []string{"a@outlook.com", "b@outlook.com", "c@outlook.com"}; !slices.Equal(got, want) {
			t.Errorf("claims = %v, want %v", got, want)
		}
		if n, ok := p.limit(); n != 0 || !ok {
			t.Errorf("limit after the claims = %d, %v, want 0, true", n, ok)
		}
	})
	t.Run("a mailbox with a domain stays in rotation", func(t *testing.T) {
		list := mailboxes("a@outlook.com", "b@example.com", "c@outlook.com")
		list[1].IMAPDomains = []string{"example.com"}
		p := newMailboxPool(emailSources(&config.Config{Email: config.EmailConfig{Mailboxes: list}}))
		if _, ok := p.limit(); ok {
			t.Error("limit has a limit, want none")
		}
		want := []string{"a@outlook.com", "b@example.com", "c@outlook.com", "b@example.com", "b@example.com"}
		if got := claimAll(p, 5); !slices.Equal(got, want) {
			t.Errorf("claims = %v, want %v", got, want)
		}
	})
	t.Run("no mailboxes list makes the email section the one mailbox", func(t *testing.T) {
		cfg := &config.Config{Email: config.EmailConfig{IMAPUser: "me@outlook.com"}}
		if got, want := claimAll(newMailboxPool(emailSources(cfg)), 10), []string{"me@outlook.com"}; !slices.Equal(got, want) {
			t.Errorf("claims = %v, want %v", got, want)
		}
	})
	t.Run("concurrent claims hand each mailbox to one claim", func(t *testing.T) {
		users := make([]string, 50)
		for i := range users {
			users[i] = "user" + strconv.Itoa(i) + "@outlook.com"
		}
		p := newMailboxPool(emailSources(&config.Config{Email: config.EmailConfig{Mailboxes: mailboxes(users...)}}))
		var mu sync.Mutex
		var got []string
		var wg sync.WaitGroup
		for range 60 {
			wg.Go(func() {
				if src, ok := p.claim(); ok {
					mu.Lock()
					got = append(got, src.IMAPUser)
					mu.Unlock()
				}
			})
		}
		waittest.Wait(t, &wg)
		slices.Sort(got)
		slices.Sort(users)
		if !slices.Equal(got, users) {
			t.Errorf("claims = %v, want each of %v once", got, users)
		}
	})
}

func TestMailboxPoolCheckLogins(t *testing.T) {
	cfg := &config.Config{Email: config.EmailConfig{Mailboxes: mailboxes("a@outlook.com", "b@outlook.com", "c@outlook.com")}}
	for _, tt := range []struct {
		name string
		n    int
		want []string
	}{
		{"the first n mailboxes", 2, []string{"a@outlook.com", "b@outlook.com"}},
		{"no mailbox for a zero n", 0, nil},
		{"every mailbox for a negative n", -1, []string{"a@outlook.com", "b@outlook.com", "c@outlook.com"}},
		{"every mailbox for an n above the count", 9, []string{"a@outlook.com", "b@outlook.com", "c@outlook.com"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var checked []string
			err := newMailboxPool(emailSources(cfg)).checkLogins(t.Context(), tt.n, func(_ context.Context, src config.MailboxConfig) error {
				mu.Lock()
				defer mu.Unlock()
				checked = append(checked, src.IMAPUser)
				return nil
			})
			slices.Sort(checked)
			if err != nil || !slices.Equal(checked, tt.want) {
				t.Errorf("checkLogins = %v, checked %v, want nil and %v", err, checked, tt.want)
			}
		})
	}
	t.Run("the error names each mailbox whose check failed", func(t *testing.T) {
		refused := errors.New("refused")
		err := newMailboxPool(emailSources(cfg)).checkLogins(t.Context(), -1, func(_ context.Context, src config.MailboxConfig) error {
			if src.IMAPUser == "b@outlook.com" {
				return nil
			}
			return refused
		})
		want := "checking the mailbox: a@outlook.com: refused\nchecking the mailbox: c@outlook.com: refused"
		if !errors.Is(err, refused) || err.Error() != want {
			t.Errorf("checkLogins = %v, want %q", err, want)
		}
	})
}

func TestMailboxPlan(t *testing.T) {
	accounts := newTestStore(t)
	created := time.Date(2026, 10, 26, 12, 21, 0, 0, time.UTC)
	if err := accounts.WriteAccount(t.Context(), &account.Account{Email: "used@outlook.com", Username: "u", Password: "p", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name      string
		email     config.EmailConfig
		count     int
		wantCount int
		wantUsed  int
		wantUsers []string
		wantErr   string // empty means no error
	}{
		{name: "a mailbox with no account", email: config.EmailConfig{IMAPUser: "new@outlook.com"}, count: 1,
			wantCount: 1, wantUsers: []string{"new@outlook.com"}},
		{name: "a mailbox with an account", email: config.EmailConfig{IMAPUser: "used@outlook.com"}, count: 1,
			wantErr: "too few mailboxes with no account: each of the 1 mailbox already has an account. Add mailboxes with no account"},
		{name: "a mailbox with an account in other letter case", email: config.EmailConfig{IMAPUser: "USED@Outlook.com"}, count: 1,
			wantErr: "too few mailboxes with no account: each of the 1 mailbox already has an account. Add mailboxes with no account"},
		{name: "a domain gives each account a new address", email: config.EmailConfig{IMAPUser: "used@outlook.com", IMAPDomains: []string{"example.com"}}, count: 5,
			wantCount: 5, wantUsers: []string{"used@outlook.com", "used@outlook.com"}},
		{name: "a domain keeps an unlimited run unlimited", email: config.EmailConfig{IMAPUser: "used@outlook.com", IMAPDomains: []string{"example.com"}}, count: -1,
			wantCount: -1, wantUsers: []string{"used@outlook.com", "used@outlook.com"}},
		{name: "the list drops a used mailbox", email: config.EmailConfig{Mailboxes: mailboxes("a@outlook.com", "used@outlook.com", "b@outlook.com")}, count: 2,
			wantCount: 2, wantUsed: 1, wantUsers: []string{"a@outlook.com", "b@outlook.com"}},
		{name: "an unlimited run gets one account for each free mailbox", email: config.EmailConfig{Mailboxes: mailboxes("a@outlook.com", "used@outlook.com", "b@outlook.com")}, count: -1,
			wantCount: 2, wantUsed: 1, wantUsers: []string{"a@outlook.com", "b@outlook.com"}},
		{name: "more accounts than free mailboxes", email: config.EmailConfig{Mailboxes: mailboxes("a@outlook.com", "used@outlook.com")}, count: 2,
			wantErr: "too few mailboxes with no account: concurrency.account_count is 2, but 1 of the 2 mailboxes has no account. Each mailbox with no imap_domains makes one account"},
		{name: "more accounts than free mailboxes, two free", email: config.EmailConfig{Mailboxes: mailboxes("a@outlook.com", "b@outlook.com", "used@outlook.com")}, count: 3,
			wantErr: "too few mailboxes with no account: concurrency.account_count is 3, but 2 of the 3 mailboxes have no account. Each mailbox with no imap_domains makes one account"},
		{name: "every mailbox of the list is used", email: config.EmailConfig{Mailboxes: mailboxes("used@outlook.com", "USED@outlook.com")}, count: -1,
			wantErr: "too few mailboxes with no account: each of the 2 mailboxes already has an account. Add mailboxes with no account"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Email: tt.email, Concurrency: config.ConcurrencyConfig{AccountCount: tt.count}}
			pool, count, used, err := mailboxPlan(t.Context(), accounts, cfg)
			if tt.wantErr != "" {
				if !errors.Is(err, errTooFewMailboxes) || err.Error() != tt.wantErr {
					t.Errorf("mailboxPlan = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("mailboxPlan = %v, want nil", err)
			}
			if count != tt.wantCount || used != tt.wantUsed {
				t.Errorf("count, used = %d, %d, want %d, %d", count, used, tt.wantCount, tt.wantUsed)
			}
			if got := claimAll(pool, len(tt.wantUsers)); !slices.Equal(got, tt.wantUsers) {
				t.Errorf("claims = %v, want %v", got, tt.wantUsers)
			}
		})
	}
	t.Run("a store that fails the read", func(t *testing.T) {
		closed := newTestStore(t)
		if err := closed.Close(); err != nil {
			t.Fatal(err)
		}
		_, _, _, err := mailboxPlan(t.Context(), closed, &config.Config{Email: config.EmailConfig{IMAPUser: "used@outlook.com"}})
		if err == nil || errors.Is(err, errTooFewMailboxes) || !strings.HasPrefix(err.Error(), "checking the mailbox used@outlook.com for an account: ") {
			t.Errorf("mailboxPlan = %v, want the wrapped read error", err)
		}
	})
}
