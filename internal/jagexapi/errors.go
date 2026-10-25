package jagexapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/sardanioss/httpcloak/client"
)

// ErrRegistrationFailed reports that the server rejected a registration or 2FA request.
var ErrRegistrationFailed = errors.New("account registration failed")

// ErrRateLimited reports that the server answered a request with status 429.
var ErrRateLimited = errors.New("rate limited")

// Refused reports whether err of SubmitPassword or ActivateTOTP is an answer that
// refused the POST: a 4xx status, a 429 or a state that fails the step. The POST
// is sent once, so the server did not make the change. Other calls also wrap
// ErrRegistrationFailed on local failures, so the report holds only for these two.
func Refused(err error) bool {
	return errors.Is(err, ErrRegistrationFailed) || errors.Is(err, ErrRateLimited)
}

// describeResponse returns a one-line summary of resp for an error: the status,
// each summary header that is set, and the body length. It logs the head of the
// body at DEBUG. Bytes closes a readable body. The caller's Close covers an unreadable one.
// The summary prints raw header values. The code decides nothing about Cloudflare or the origin.
// It never names the URL, because an oauth2/auth URL holds a one-time verifier.
func describeResponse(ctx context.Context, logger *slog.Logger, resp *client.Response) string {
	var b strings.Builder
	fmt.Fprintf(&b, "status %d", resp.StatusCode)
	for _, name := range [...]string{"retry-after", "cf-mitigated", "server", "cf-ray", "content-type"} {
		if value := resp.GetHeader(name); value != "" {
			fmt.Fprintf(&b, ", %s: %s", name, value)
		}
	}
	body, err := resp.Bytes()
	if err != nil {
		fmt.Fprintf(&b, ", body: unreadable: %v", err)
		return b.String()
	}
	logger.DebugContext(ctx, "error response", "status", resp.StatusCode, "body", logSnippet(body))
	fmt.Fprintf(&b, ", body: %d bytes", len(body))
	return b.String()
}

// logSnippet returns at most the first 512 bytes of a response body, for a log line.
func logSnippet(body []byte) string {
	return string(body[:min(len(body), 512)])
}
