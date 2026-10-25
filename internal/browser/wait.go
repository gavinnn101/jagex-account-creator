package browser

import (
	"context"
	"fmt"
	"time"
)

// Wait waits until both sources have set cf_clearance. On each click tick, it
// clicks the Turnstile checkbox if the page is a challenge. Cloudflare can
// show a challenge at any point in the redirect chain. One select receives done
// and both tickers, so the return waits for no tick. When ctx ends first, it
// returns ctx.Err(). When timeout passes first, it returns ErrChallengeTimeout.
// Limitation: if Cloudflare serves no challenge, the gate never sees the challenge source and times out.
// Limitation: the challenge page's commit can cancel a JSD report. JSD then reports on the next HTML page, and timeout is the backstop.
func (g *ClearanceGate) Wait(ctx context.Context, timeout time.Duration, debug bool) error {
	g.logger.Debug("waiting for clearance")

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Limitation: an interactive challenge gets its click on the next 1 s tick, not when it appears.
	clickTicker := time.NewTicker(1 * time.Second)
	defer clickTicker.Stop()

	diagTicker := time.NewTicker(10 * time.Second)
	defer diagTicker.Stop()

	diagCount := 0
	for {
		select {
		case <-g.done:
			return nil
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// The parent ctx, not the expired waitCtx, still serves diagnostics.
			LogPageState(ctx, g.c, g.logger, "clearance wait timeout")
			return ErrChallengeTimeout
		case <-diagTicker.C:
			if waitCtx.Err() != nil {
				continue
			}
			diagCount++
			LogPageState(waitCtx, g.c, g.logger, fmt.Sprintf("clearance wait tick %d", diagCount))
			SaveDebugScreenshot(waitCtx, g.c, g.logger, debug, fmt.Sprintf("clearance_wait_%d", diagCount))
		case <-clickTicker.C:
			if waitCtx.Err() != nil {
				continue
			}
			g.clickIfChallenge(waitCtx)
		}
	}
}

// clickIfChallenge clicks the Turnstile checkbox when the page title marks a
// challenge. An unreadable title reads as a challenge, so the loop tries a
// click rather than skipping one.
func (g *ClearanceGate) clickIfChallenge(ctx context.Context) {
	if isChallengeTitle(pageTitle(ctx, g.c), g.challengeTitle) {
		g.logger.Debug("challenge detected during wait, clicking", "waiting_for", "clearance")
		if err := clickTurnstile(ctx, g.c, g.logger); err != nil {
			g.logger.Debug("Turnstile click failed", "waiting_for", "clearance", "error", err)
		}
	}
}
