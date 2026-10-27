package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"golang.org/x/sync/errgroup"

	"jagex_account_creator/internal/config"
	"jagex_account_creator/internal/email"
	"jagex_account_creator/internal/store"
)

// errTooFewMailboxes is the error of a run whose mailboxes with no account cannot hold its account count.
var errTooFewMailboxes = errors.New("too few mailboxes with no account")

// errNoMailboxLeft is the error of an account that finds no mailbox in the pool.
// mailboxPlan caps the account count of a run at its mailboxes, so it marks a bug.
var errNoMailboxLeft = errors.New("no mailbox left for the account")

// maxLoginChecks is the number of mailbox logins that a run start checks at the same time.
const maxLoginChecks = 8

// mailboxPool hands each account of a run its mailbox. A mailbox with no
// imap_domains has one address, and Jagex registers an address once, so the pool
// hands it out once. A mailbox with a domain gives each account a new random
// address at one of its domains, so the pool hands it out again, in rotation. It is safe for
// concurrent use.
type mailboxPool struct {
	mu      sync.Mutex
	sources []config.MailboxConfig // every reusable mailbox, and each single-use one that is not handed out yet
	next    int                    // the index of the next claim, modulo len(sources)
}

// newMailboxPool returns a pool of sources, in order.
func newMailboxPool(sources []config.MailboxConfig) *mailboxPool {
	return &mailboxPool{sources: slices.Clone(sources)}
}

// claim returns the next mailbox, and false when the pool has none left. A
// single-use mailbox leaves the pool.
func (p *mailboxPool) claim() (config.MailboxConfig, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.sources) == 0 {
		return config.MailboxConfig{}, false
	}
	k := p.next % len(p.sources)
	src := p.sources[k]
	if len(src.IMAPDomains) == 0 {
		// next stays at k, which now names the mailbox after src.
		p.sources = slices.Delete(p.sources, k, k+1)
		p.next = k
	} else {
		p.next = k + 1
	}
	return src, true
}

// workerMailbox is the mailbox of one worker. The worker keeps it across the
// retries of one account. Once the account is in the store, a mailbox with no
// domains has made its one account, so the next account claims a new one.
type workerMailbox struct {
	pool     *mailboxPool
	logger   *slog.Logger
	provider *email.IMAPProvider
}

// get returns the provider of the mailbox, and claims one when the worker has none.
// The provider gets the logger of the worker, so its log lines carry the worker.
func (w *workerMailbox) get(ctx context.Context) (*email.IMAPProvider, error) {
	if w.provider == nil {
		src, ok := w.pool.claim()
		if !ok {
			return nil, errNoMailboxLeft
		}
		w.provider = buildProviderFromSource(ctx, w.logger, src)
	}
	return w.provider, nil
}

// done ends the account of an attempt that returned err, when its account is in the store.
func (w *workerMailbox) done(err error) {
	if (err == nil || errors.Is(err, errAccountWritten)) && len(w.provider.Domains) == 0 {
		w.provider = nil
	}
}

// limit returns how many accounts the pool can still give a mailbox, and false
// when it has no limit, because it holds a mailbox with domains.
func (p *mailboxPool) limit() (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, src := range p.sources {
		if len(src.IMAPDomains) > 0 {
			return 0, false
		}
	}
	return len(p.sources), true
}

// checkLogins runs check on the mailboxes that the first n claims hand out, at
// most maxLoginChecks at a time. A negative n checks every mailbox. The error
// names the address of each mailbox whose check failed.
func (p *mailboxPool) checkLogins(ctx context.Context, n int, check func(ctx context.Context, src config.MailboxConfig) error) error {
	p.mu.Lock()
	sources := p.sources
	if n >= 0 {
		sources = sources[:min(n, len(sources))]
	}
	sources = slices.Clone(sources)
	p.mu.Unlock()

	errs := loginErrors(ctx, sources, check)
	for i, err := range errs {
		if err != nil {
			errs[i] = fmt.Errorf("checking the mailbox: %s: %w", sources[i].IMAPUser, err)
		}
	}
	return errors.Join(errs...)
}

// loginErrors runs check on each mailbox of sources, at most maxLoginChecks at
// a time. It returns the error of each check, in the order of sources.
func loginErrors(ctx context.Context, sources []config.MailboxConfig, check func(ctx context.Context, src config.MailboxConfig) error) []error {
	errs := make([]error, len(sources))
	var g errgroup.Group
	g.SetLimit(maxLoginChecks)
	for i, src := range sources {
		g.Go(func() error {
			errs[i] = check(ctx, src)
			return nil
		})
	}
	_ = g.Wait() // Each check keeps its error in errs, so the wait has none.
	return errs
}

// mailboxPlan returns the mailbox pool of a run of cfg and the account count of
// that run. A mailbox with no imap_domains that holds an account in accounts
// leaves the pool, because Jagex registers an address once, so such a run would
// only wait for a verification email that never comes. used is the count of
// such mailboxes. With a pool that has a limit, an unlimited run gets the limit
// as its count. The error matches errTooFewMailboxes when no mailbox is left,
// or when the count is above the limit.
func mailboxPlan(ctx context.Context, accounts *store.Store, cfg *config.Config) (pool *mailboxPool, count, used int, err error) {
	all := emailSources(cfg)
	free := make([]config.MailboxConfig, 0, len(all))
	for _, src := range all {
		if len(src.IMAPDomains) == 0 {
			switch _, readErr := accounts.AccountByEmail(ctx, src.IMAPUser); {
			case readErr == nil:
				used++
				continue
			case !errors.Is(readErr, store.ErrNotFound):
				return nil, 0, 0, fmt.Errorf("checking the mailbox %s for an account: %w", src.IMAPUser, readErr)
			}
		}
		free = append(free, src)
	}
	pool = newMailboxPool(free)
	count = cfg.Concurrency.AccountCount
	limit, limited := pool.limit()
	switch {
	case !limited:
	case limit == 0:
		return nil, 0, 0, fmt.Errorf("%w: each of the %d %s already has an account. Add mailboxes with no account",
			errTooFewMailboxes, len(all), mailboxNoun(len(all)))
	case count == -1:
		count = limit
	case count > limit:
		return nil, 0, 0, fmt.Errorf("%w: concurrency.account_count is %d, but %d of the %d %s %s no account. "+
			"Each mailbox with no imap_domains makes one account", errTooFewMailboxes, count, limit, len(all), mailboxNoun(len(all)), hasNoun(limit))
	}
	return pool, count, used, nil
}

// mailboxNoun returns mailbox or mailboxes for a count of n.
func mailboxNoun(n int) string {
	if n == 1 {
		return "mailbox"
	}
	return "mailboxes"
}

// hasNoun returns has or have for a count of n.
func hasNoun(n int) string {
	if n == 1 {
		return "has"
	}
	return "have"
}
