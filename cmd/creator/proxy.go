package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"jagex_account_creator/internal/browser"
	"jagex_account_creator/internal/config"
	"jagex_account_creator/internal/proxy"
	"jagex_account_creator/internal/store"
)

// runProxies returns the proxy manager of a run of cfg. With proxy.direct set,
// the run connects direct and ignores the list. Else it uses the enabled
// proxies of accounts, and a list with none is an error.
func runProxies(ctx context.Context, logger *slog.Logger, cfg *config.Config, accounts *store.Store) (*proxy.Manager, error) {
	if cfg.Proxy.Direct {
		logger.Warn("proxy.direct is set, using direct connection")
		return proxy.NewManager(nil), nil
	}
	proxies, err := accounts.EnabledProxies(ctx)
	if err != nil {
		return nil, err
	}
	if len(proxies) == 0 {
		return nil, errors.New("no enabled proxy: import or enable a proxy, or set proxy.direct")
	}
	logger.Info("proxies loaded", "count", len(proxies))
	return proxy.NewManager(proxies), nil
}

// startForwardProxy starts a local forward proxy for p and returns it with its
// listen address. The caller must stop it. A direct connection gets no forward
// proxy and no allowlist, so it returns nil and "".
func startForwardProxy(ctx context.Context, logger *slog.Logger, p proxy.Proxy) (*proxy.ForwardProxy, string, error) {
	if p.IsZero() {
		return nil, "", nil
	}
	fwd, err := proxy.NewForwardProxy(ctx, p, logger)
	if err != nil {
		return nil, "", fmt.Errorf("starting forward proxy: %w", err)
	}
	addr := fwd.Addr()
	return fwd, addr, nil
}

// forwardProxyURL returns the proxy URL of fwd for the HTTP client, or "" for a
// direct connection. httpcloak parses the URL, so the URL needs a scheme.
func forwardProxyURL(fwd *proxy.ForwardProxy) string {
	if fwd == nil {
		return ""
	}
	return "http://" + fwd.Addr()
}

// errProxyCooled matches an error whose attempt reported its proxy, which is
// now in cooldown. A worker loop retries such an error at once, because the
// next pick skips the proxy or waits for the cooldown end.
var errProxyCooled = errors.New("proxy in cooldown")

// coolProxy reports err as a failure of the proxy of the attempt and returns
// err marked as cooled. A caller reports only a failure that the end of its own
// context did not cause. The WARN and the run state name the reason only when
// the report starts a cooldown, because a report during a running cooldown
// repeats one incident. A direct connection has no proxy to cool, so it returns
// err unchanged.
func (a *attempt) coolProxy(reason string, err error) error {
	if a.p.IsZero() {
		return err
	}
	if cooldown, failures, started := a.run.proxyMgr.ReportFailure(a.p); started {
		a.logger.Warn("proxy in cooldown",
			"reason", reason,
			"proxy", a.p.HostPort(),
			"error", err,
			"cooldown", cooldown,
			"failures", failures,
		)
		a.worker.state.proxyCooled(a.p.HostPort(), reason, time.Now().Add(cooldown), failures)
	}
	return markedError{err: err, mark: errProxyCooled}
}

// checkUpstream checks the upstream through browserFwd before Chrome launches.
// A direct connection has no forward proxy and no check. A proxy that fails the
// check goes into cooldown, and checkUpstream stops browserFwd. A check that the
// end of ctx cut short cools nothing, because it says nothing about the proxy.
func (a *attempt) checkUpstream(ctx context.Context, browserFwd *proxy.ForwardProxy) error {
	if browserFwd == nil {
		return nil
	}
	err := browserFwd.CheckUpstream(ctx)
	if err == nil {
		return nil
	}
	// The check's own timeout leaves ctx alive, so a dead proxy still cools.
	if ctx.Err() != nil {
		err = fmt.Errorf("proxy connectivity check: %w", err)
	} else {
		err = a.coolProxy("connectivity check", fmt.Errorf("proxy connectivity check: %w: %w", browser.ErrProxyConnection, err))
	}
	browserFwd.Stop()
	return err
}
