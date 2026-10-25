package jagexapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"os"
	"path"
	"time"

	"github.com/sardanioss/httpcloak/client"
)

const (
	accountHost = "account.jagex.com"
	baseURL     = "https://" + accountHost
	// siteLocale is the locale of the two SPAs: the path segment of their pages, the locale and lang
	// query values, and the locale header of their API calls. The login SPA's first call sends none.
	siteLocale = "en-GB"

	registrationBaseURL    = baseURL + "/login/api/registrations/regular"
	continueURL            = baseURL + "/login/api/registrations/regular/continue"
	statesURL              = baseURL + "/login/api/registrations/regular/states"
	validateDisplayName    = baseURL + "/login/api/registrations/regular/displayNames/validate"
	currentRegistrationURL = baseURL + "/login/api/registrations/regular/current"
	managementURL          = baseURL + "/" + siteLocale + "/manage"
	totpURL                = baseURL + "/manage/api/users/mfa/totp"

	// ManageRegistrationURL is the portal page that starts a registration. It is Chrome's target and the start chain's Referer.
	ManageRegistrationURL = managementURL + "?flow=registration"
	oauthLoginURL         = baseURL + "/manage/api/oauth/login?flow=registration&locale=" + siteLocale

	registrationContinueURL  = baseURL + "/" + siteLocale + "/login/registration-continue"
	registrationCompletedURL = baseURL + "/" + siteLocale + "/login/registration-completed"
	consentURL               = baseURL + "/api/auth/consent"
	tokenURL                 = baseURL + "/manage/api/oauth/token?lang=" + siteLocale
)

// The screens of the login SPA after the email form. Each is a path beside the landing page.
const (
	screenVerify      = "registration-verify"
	screenAccountName = "registration-complete/account-name"
	screenPassword    = "registration-complete/password"
)

// RegistrationClient handles the Jagex account registration HTTP flow.
type RegistrationClient struct {
	client         *Client
	logger         *slog.Logger
	landing        *url.URL // the login page that the start chain reached
	page           string   // the URL of the current screen. Referer for every registration XHR
	loginChallenge string
	// current is the last full answer of /current. Chrome's HTTP cache keeps it for the whole page session.
	current struct{ state, etag string }
}

// newRegistrationClient returns a registration client on c.
func newRegistrationClient(c *Client, logger *slog.Logger) *RegistrationClient {
	return &RegistrationClient{
		client: c,
		logger: logger.With("component", "registration"),
	}
}

// LoginLink asks the player portal for the /oauth2/auth link that starts a
// registration. The request needs no cf_clearance.
func LoginLink(ctx context.Context, c *Client, logger *slog.Logger) (*url.URL, error) {
	rc := newRegistrationClient(c, logger)

	link, err := rc.loginLink(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting the login link: %w", err)
	}
	return link, nil
}

// StartRegistration follows loginLink and the login challenge redirect over
// HTTP. It returns a registration client for the login page that the two hops
// reach. c must hold a cf_clearance, because Cloudflare challenges the
// /oauth2/auth hop. loginLink can come from before the cookie import.
func StartRegistration(ctx context.Context, c *Client, logger *slog.Logger, loginLink *url.URL) (*RegistrationClient, error) {
	rc := newRegistrationClient(c, logger)

	challengePage, err := rc.followRedirect(ctx, navigationShape(false), loginLink.String(), ManageRegistrationURL, "login_challenge")
	if err != nil {
		return nil, fmt.Errorf("following the login link: %w", err)
	}
	landing, err := rc.followRedirect(ctx, redirectShape(false), challengePage.String(), ManageRegistrationURL, "login_challenge")
	if err != nil {
		return nil, fmt.Errorf("following the login challenge: %w", err)
	}
	// The registration API calls send the landing page as their Referer, so it must be on the API host.
	if landing.Host != accountHost {
		return nil, fmt.Errorf("following the login challenge: %w: the login page is not on %s", ErrRegistrationFailed, accountHost)
	}
	rc.landing = landing
	rc.page = landing.String()
	rc.loginChallenge = landing.Query().Get("login_challenge")
	return rc, nil
}

// enterScreen moves the client to screen, as the page's pushState does. The
// screen URL keeps the query of the landing page, which holds the login challenge.
func (rc *RegistrationClient) enterScreen(screen string) {
	u := *rc.landing
	u.Path = path.Join(path.Dir(rc.landing.Path), screen)
	rc.page = u.String()
}

// loginLink sends the login-link request and reads the link from its answer.
func (rc *RegistrationClient) loginLink(ctx context.Context) (*url.URL, error) {
	resp, err := rc.getJSON(ctx, loginLinkShape(), oauthLoginURL, ManageRegistrationURL, "")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Close() }()

	if err := rc.requireSuccess(ctx, resp); err != nil {
		return nil, err
	}
	var result struct {
		LoginLink string `json:"loginLink"`
	}
	if err := resp.JSON(&result); err != nil {
		return nil, fmt.Errorf("%w: decoding login link response: %w", ErrRegistrationFailed, err)
	}
	if result.LoginLink == "" {
		return nil, fmt.Errorf("%w: login link response has no loginLink", ErrRegistrationFailed)
	}
	return resolveLocation(oauthLoginURL, result.LoginLink)
}

// InitRegistration calls the base registration endpoint to initialize the
// registration state machine. The page starts the process by posting the login
// challenge it was landed with.
func (rc *RegistrationClient) InitRegistration(ctx context.Context) error {
	payload := map[string]string{"loginChallenge": rc.loginChallenge}

	resp, err := rc.postJSON(ctx, xhrPostShape(), registrationBaseURL, rc.page, payload, "")
	if err != nil {
		return fmt.Errorf("initializing registration: %w", err)
	}
	defer func() { _ = resp.Close() }()

	if err := rc.requireNotError(ctx, resp); err != nil {
		return fmt.Errorf("initializing registration: %w", err)
	}
	rc.logBody(ctx, "registration init response", resp)
	return nil
}

// SubmitInitialRegistration sends the email and DOB to start the registration,
// then enters the verify screen. The DOB format must be "YYYY-MM-DDT00:00:00.000Z".
func (rc *RegistrationClient) SubmitInitialRegistration(ctx context.Context, email, dob string) error {
	payload := map[string]any{
		"loginChallenge": rc.loginChallenge,
		"data": map[string]string{
			"email": email,
			"dob":   dob,
		},
	}

	rc.client.DebugLogCookies(continueURL)

	resp, err := rc.postJSON(ctx, localeXHRPostShape(), continueURL, rc.page, payload, "")
	if err != nil {
		return fmt.Errorf("submitting initial registration: %w", err)
	}
	defer func() { _ = resp.Close() }()

	if err := rc.requireNotError(ctx, resp); err != nil {
		if resp.StatusCode == 403 {
			body, _ := resp.Bytes()
			rc.saveDebugResponse(ctx, body)
		}
		return fmt.Errorf("submitting initial registration: %w", err)
	}
	rc.logBody(ctx, "initial registration response", resp)
	rc.enterScreen(screenVerify)
	return nil
}

// SubmitVerificationCode posts the code, polls /current until FINALIZING, then
// enters the account-name screen and sends its GET.
// Limitation: a state that never reaches FINALIZING polls until ctx ends, as the page does.
func (rc *RegistrationClient) SubmitVerificationCode(ctx context.Context, code string) error {
	if err := rc.postCode(ctx, code); err != nil {
		return fmt.Errorf("submitting verification code: %w", err)
	}
	err := rc.pollCurrent(ctx, func(state string) (bool, error) { return state == stateFinalizing, nil })
	if err != nil {
		return err
	}
	rc.enterScreen(screenAccountName)
	_, err = rc.readCurrent(ctx)
	return err
}

// postCode posts the verification code from the current screen.
func (rc *RegistrationClient) postCode(ctx context.Context, code string) error {
	resp, err := rc.postJSON(ctx, localeXHRPostShape(), statesURL, rc.page, map[string]string{"code": code}, "")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Close() }()
	return rc.requireNotError(ctx, resp)
}

// ValidateDisplayName posts the name. When the name is valid, it enters the
// password screen and sends its GET. A name that is not valid fails.
func (rc *RegistrationClient) ValidateDisplayName(ctx context.Context, displayName string) error {
	if err := rc.validateName(ctx, displayName); err != nil {
		return fmt.Errorf("validating display name: %w", err)
	}
	rc.enterScreen(screenPassword)
	_, err := rc.readCurrent(ctx)
	return err
}

// validateName posts displayName from the current screen. It fails unless the answer marks the name valid.
func (rc *RegistrationClient) validateName(ctx context.Context, displayName string) error {
	resp, err := rc.postJSON(ctx, localeXHRPostShape(), validateDisplayName, rc.page, map[string]string{"displayName": displayName}, "")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Close() }()

	if err := rc.requireNotError(ctx, resp); err != nil {
		return err
	}
	// The errors stay raw JSON, because no capture shows their form on a name that is not valid.
	var result struct {
		Valid  bool            `json:"valid"`
		Errors json.RawMessage `json:"errors"`
	}
	if err := resp.JSON(&result); err != nil {
		return fmt.Errorf("%w: decoding the validation answer: %w", ErrRegistrationFailed, err)
	}
	if !result.Valid {
		// string keeps an absent value empty: RawMessage's String prints null for nil.
		return fmt.Errorf("%w: the name is not valid: %s", ErrRegistrationFailed, string(result.Errors))
	}
	return nil
}

// The registration states that the flow reads.
const (
	stateFinalizing = "REGULAR_REGISTRATION_FINALIZING"
	stateCreating   = "AWAITING_USER_DETAILS_CREATING"
	stateCompleted  = "COMPLETED"
)

// pagePollInterval is the interval of the page's short poll of /current.
const pagePollInterval = 2 * time.Second

// registrationState is the answer of the password step and of /current.
// Data is the data object of the answer, read only for the reason of a refused password.
type registrationState struct {
	State string          `json:"state"`
	Data  json.RawMessage `json:"data"`
}

// reason returns the reason field of Data as raw JSON text.
func (s registrationState) reason() string {
	var data struct {
		Reason json.RawMessage `json:"reason"`
	}
	// No capture shows the form of data on a rollback, so a data that is not an object gives an empty reason.
	_ = json.Unmarshal(s.Data, &data)
	// string keeps an absent reason empty: RawMessage's String prints null for nil.
	return string(data.Reason)
}

// classifyState reports whether state is still pending. Any state other than
// creating or completed means the registration failed.
func classifyState(state string) (pending bool, err error) {
	switch state {
	case stateCompleted:
		return false, nil
	case stateCreating:
		return true, nil
	default:
		return false, fmt.Errorf("%w: registration state %q", ErrRegistrationFailed, state)
	}
}

// SubmitPassword sends the display name, the password and the consent flags
// from the current screen. It fails unless the state is creating or completed.
// The error of any other state carries the reason of the answer as raw JSON.
func (rc *RegistrationClient) SubmitPassword(ctx context.Context, displayName, password string) error {
	payload := map[string]any{
		"displayName":       displayName,
		"password":          password,
		"receiveEmails":     false,
		"thirdPartyConsent": false,
	}

	resp, err := rc.postJSON(ctx, localeXHRPostShape(), statesURL, rc.page, payload, "")
	if err != nil {
		return fmt.Errorf("submitting password: %w", err)
	}
	defer func() { _ = resp.Close() }()

	if err := rc.requireNotError(ctx, resp); err != nil {
		return fmt.Errorf("submitting password: %w", err)
	}

	// A 2xx body that does not decode is no refusal, so the error has no sentinel.
	var result registrationState
	if err := resp.JSON(&result); err != nil {
		return fmt.Errorf("decoding password response: %w", err)
	}
	rc.logger.Debug("password submitted", "state", result.State)
	if _, err := classifyState(result.State); err != nil {
		return fmt.Errorf("submitting password: %w, reason: %s", err, result.reason())
	}
	return nil
}

// WaitForAccountCreation polls /current until the state is completed, as the
// page does after the password POST. It fails at once on a state that is
// neither creating nor completed.
// Limitation: the page also stops on FINALIZING and FAILED. This poll fails on them.
func (rc *RegistrationClient) WaitForAccountCreation(ctx context.Context) error {
	return rc.pollCurrent(ctx, func(state string) (bool, error) {
		pending, err := classifyState(state)
		if err != nil {
			return false, fmt.Errorf("waiting for account creation: %w", err)
		}
		return !pending, nil
	})
}

// pollCurrent reads /current at once, then pagePollInterval after each answer,
// until stop reports true or fails. This is the page's short poll.
func (rc *RegistrationClient) pollCurrent(ctx context.Context, stop func(state string) (bool, error)) error {
	for {
		state, err := rc.readCurrent(ctx)
		if err != nil {
			return err
		}
		done, err := stop(state)
		if err != nil || done {
			return err
		}

		timer := time.NewTimer(pagePollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("polling the registration state: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

// readCurrent sends one GET of /current from the current screen. It sends the
// ETag of the last full answer, as Chrome's HTTP cache does, so a 304 keeps the last state.
func (rc *RegistrationClient) readCurrent(ctx context.Context) (string, error) {
	resp, err := rc.getJSON(ctx, currentGetShape(), currentRegistrationURL, rc.page, rc.current.etag)
	if err != nil {
		return "", fmt.Errorf("reading the registration state: %w", err)
	}
	defer func() { _ = resp.Close() }()

	if err := rc.requireNotError(ctx, resp); err != nil {
		return "", fmt.Errorf("reading the registration state: %w", err)
	}
	if resp.StatusCode != 304 {
		var result registrationState
		if err := resp.JSON(&result); err != nil {
			return "", fmt.Errorf("%w: decoding the registration state: %w", ErrRegistrationFailed, err)
		}
		rc.current.state, rc.current.etag = result.State, resp.GetHeader("etag")
	}
	rc.logger.Debug("registration state", "state", rc.current.state, "status", resp.StatusCode)
	return rc.current.state, nil
}

// Login logs the registered account in, so that the TOTP calls are authorized.
// It follows the redirects of the registration-completed page, accepts the
// consent, and exchanges the code for the session-token cookie.
// The continue button is a link, so its page load and the redirect that follows it are clicked.
// Limitation: the navigation captures predict the two clicked hops, their referer and the unclicked consent-verifier hop.
// No capture holds these hops. The consent-verifier hop assumes that the page load after the consent XHR has no user activation.
func (rc *RegistrationClient) Login(ctx context.Context) error {
	authURL, err := rc.followRedirect(ctx, navigationShape(true), registrationContinueURL, registrationCompletedURL, "login_verifier")
	if err != nil {
		return fmt.Errorf("continuing registration: %w", err)
	}
	consentPage, err := rc.followRedirect(ctx, redirectShape(true), authURL.String(), registrationCompletedURL, "consent_challenge")
	if err != nil {
		return fmt.Errorf("following the login verifier: %w", err)
	}
	verifierURL, err := rc.acceptConsent(ctx, consentPage)
	if err != nil {
		return fmt.Errorf("accepting consent: %w", err)
	}
	codePage, err := rc.followRedirect(ctx, navigationShape(false), verifierURL.String(), consentPage.String(), "code", "state", "scope")
	if err != nil {
		return fmt.Errorf("following the consent verifier: %w", err)
	}
	if err := rc.exchangeCode(ctx, codePage); err != nil {
		return fmt.Errorf("exchanging the code: %w", err)
	}
	return nil
}

// acceptConsent posts the consent challenge of consentPage. It returns the
// consent verifier URL that the answer names.
func (rc *RegistrationClient) acceptConsent(ctx context.Context, consentPage *url.URL) (*url.URL, error) {
	payload := map[string]string{"consentChallenge": consentPage.Query().Get("consent_challenge")}

	resp, err := rc.postJSON(ctx, xhrPostShape(), consentURL, consentPage.String(), payload, "")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Close() }()

	if err := rc.requireSuccess(ctx, resp); err != nil {
		return nil, err
	}
	var result struct {
		RedirectTo string `json:"redirectTo"`
	}
	if err := resp.JSON(&result); err != nil {
		return nil, fmt.Errorf("%w: decoding consent response: %w", ErrRegistrationFailed, err)
	}
	if result.RedirectTo == "" {
		return nil, fmt.Errorf("%w: consent response has no redirectTo", ErrRegistrationFailed)
	}
	next, err := resolveLocation(consentURL, result.RedirectTo)
	if err != nil {
		return nil, err
	}
	if err := requireQuery(next, "consent_verifier"); err != nil {
		return nil, err
	}
	return next, nil
}

// exchangeCode posts the code, state and scope of codePage to the token
// endpoint, which sets the session-token cookie.
func (rc *RegistrationClient) exchangeCode(ctx context.Context, codePage *url.URL) error {
	q := codePage.Query()
	payload := map[string]string{"state": q.Get("state"), "code": q.Get("code"), "scope": q.Get("scope")}

	resp, err := rc.postJSON(ctx, localeXHRPostShape(), tokenURL, codePage.String(), payload, "")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Close() }()
	return rc.requireSuccess(ctx, resp)
}

// followRedirect sends a page-load GET of shape for target and returns the
// resolved Location of the redirect that answers it. The Location must carry a
// value for each key.
func (rc *RegistrationClient) followRedirect(ctx context.Context, shape requestShape, target, referer string, keys ...string) (*url.URL, error) {
	req := rc.newRequest(shape, "GET", target, nil, map[string]string{"referer": referer})

	resp, err := rc.send(ctx, req, requestPath(target))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Close() }()

	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%w, want a redirect", rc.statusError(ctx, resp))
	}
	location := resp.GetHeader("Location")
	if location == "" {
		return nil, fmt.Errorf("%w, no Location", rc.statusError(ctx, resp))
	}
	next, err := resolveLocation(target, location)
	if err != nil {
		return nil, err
	}
	if err := requireQuery(next, keys...); err != nil {
		return nil, err
	}
	return next, nil
}

// resolveLocation resolves ref, a redirect target, against base, the URL of
// the request that the redirect answers.
func resolveLocation(base, ref string) (*url.URL, error) {
	// The errors drop the parse error, because it quotes the URL and its query holds one-time tokens.
	b, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("%w: the request URL does not parse", ErrRegistrationFailed)
	}
	r, err := url.Parse(ref)
	if err != nil {
		return nil, fmt.Errorf("%w: the redirect target does not parse", ErrRegistrationFailed)
	}
	return b.ResolveReference(r), nil
}

// requestPath returns the path of target for an error message or a log line, or ""
// when target does not parse. The query stays out, because it can hold a one-time verifier.
func requestPath(target string) string {
	u, err := url.Parse(target)
	if err != nil {
		return ""
	}
	return u.Path
}

// requireQuery fails unless u carries a non-empty value for each key.
func requireQuery(u *url.URL, keys ...string) error {
	q := u.Query()
	for _, key := range keys {
		if q.Get(key) == "" {
			return fmt.Errorf("%w: %s carries no %s", ErrRegistrationFailed, u.Path, key)
		}
	}
	return nil
}

// requireSuccess fails unless resp has a 2xx status.
func (rc *RegistrationClient) requireSuccess(ctx context.Context, resp *client.Response) error {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return rc.statusError(ctx, resp)
	}
	return nil
}

// requireNotError fails when resp has a 4xx or 5xx status.
func (rc *RegistrationClient) requireNotError(ctx context.Context, resp *client.Response) error {
	if resp.StatusCode >= 400 {
		return rc.statusError(ctx, resp)
	}
	return nil
}

// statusError returns the ErrRegistrationFailed error of a response whose status fails its request.
func (rc *RegistrationClient) statusError(ctx context.Context, resp *client.Response) error {
	return fmt.Errorf("%w: %s", ErrRegistrationFailed, describeResponse(ctx, rc.logger, resp))
}

// TOTPSetupResponse is the response when initiating TOTP setup.
type TOTPSetupResponse struct {
	ID     string `json:"id"`
	Secret string `json:"secret"`
}

// SetupTOTP initiates 2FA TOTP setup and returns the TOTP ID and secret.
func (rc *RegistrationClient) SetupTOTP(ctx context.Context, email string) (*TOTPSetupResponse, error) {
	payload := map[string]string{"label": email}

	resp, err := rc.postJSON(ctx, localeXHRPostShape(), totpURL, managementURL, payload, jsonOnlyAccept)
	if err != nil {
		return nil, fmt.Errorf("setting up TOTP: %w", err)
	}
	defer func() { _ = resp.Close() }()

	if err := rc.requireNotError(ctx, resp); err != nil {
		return nil, fmt.Errorf("setting up TOTP: %w", err)
	}

	var result TOTPSetupResponse
	if err := resp.JSON(&result); err != nil {
		return nil, fmt.Errorf("%w: decoding TOTP setup response: %w", ErrRegistrationFailed, err)
	}
	return &result, nil
}

// ActivateTOTPResponse is the response from TOTP activation.
type ActivateTOTPResponse struct {
	BackupCodes []string `json:"backupCodes"`
}

// ActivateTOTP activates 2FA with the given TOTP ID and verification code.
func (rc *RegistrationClient) ActivateTOTP(ctx context.Context, totpID, verificationCode string) ([]string, error) {
	activationURL := fmt.Sprintf("%s/%s/activations", totpURL, totpID)
	payload := map[string]string{"verificationCode": verificationCode}

	resp, err := rc.postJSON(ctx, localeXHRPostShape(), activationURL, managementURL, payload, jsonOnlyAccept)
	if err != nil {
		return nil, fmt.Errorf("activating TOTP: %w", err)
	}
	defer func() { _ = resp.Close() }()

	if err := rc.requireNotError(ctx, resp); err != nil {
		return nil, fmt.Errorf("activating TOTP: %w", err)
	}

	// A 2xx body that does not decode is no refusal, so the error has no sentinel and the caller keeps the TOTP secret.
	var result ActivateTOTPResponse
	if err := resp.JSON(&result); err != nil {
		return nil, fmt.Errorf("decoding TOTP activation response: %w", err)
	}
	return result.BackupCodes, nil
}

// newRequest makes a request of shape for target. It sends a header for each
// name of the shape's order that has a value, and no other header. A value in
// own wins over a value of the shape. The order also names cookie and
// content-length, which the transport adds.
func (rc *RegistrationClient) newRequest(shape requestShape, method, target string, body []byte, own map[string]string) *client.Request {
	values := maps.Clone(rc.client.values)
	maps.Copy(values, shape.values)
	maps.Copy(values, own)

	headers := make(map[string][]string, len(shape.order))
	for _, name := range shape.order {
		if value, ok := values[name]; ok {
			headers[name] = []string{value}
		}
	}
	req := &client.Request{Method: method, URL: target, Headers: headers, HeaderOrder: shape.order}
	if body != nil {
		req.Body = bytes.NewReader(body)
	}
	return req
}

// postJSON sends payload as JSON in an XHR POST of shape. A non-empty accept
// replaces the accept of the shape. It sends the POST once.
func (rc *RegistrationClient) postJSON(ctx context.Context, shape requestShape, url, referer string, payload any, accept string) (*client.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshalling request body: %w", err)
	}
	own := map[string]string{"referer": referer}
	if accept != "" {
		own["accept"] = accept
	}

	resp, err := rc.send(ctx, rc.newRequest(shape, "POST", url, body, own), url)
	if err != nil {
		return nil, err
	}
	rc.logger.Debug("POST response", "url", url, "status", resp.StatusCode, "protocol", resp.Protocol)
	return resp, nil
}

// getJSON sends an XHR GET of shape. A non-empty ifNoneMatch goes out as
// if-none-match. It retries on transient transport errors and 5xx server errors.
func (rc *RegistrationClient) getJSON(ctx context.Context, shape requestShape, url, referer, ifNoneMatch string) (*client.Response, error) {
	own := map[string]string{"referer": referer}
	if ifNoneMatch != "" {
		own["if-none-match"] = ifNoneMatch
	}
	req := rc.newRequest(shape, "GET", url, nil, own)

	resp, err := rc.send(ctx, req, url)
	if err != nil {
		return nil, err
	}
	rc.logger.Debug("GET response", "url", url, "status", resp.StatusCode)
	return resp, nil
}

// send sends req through doWithRetry, which resends only a GET. The error text
// carries name, so the caller passes the path for a hop whose query holds a verifier.
func (rc *RegistrationClient) send(ctx context.Context, req *client.Request, name string) (*client.Response, error) {
	resp, err := doWithRetry(ctx, rc.logger, req, rc.client.Do, defaultRetryConfig())
	if err != nil {
		return nil, fmt.Errorf("executing request to %s: %w", name, err)
	}
	return resp, nil
}

// logBody logs the status and the head of the body of resp at DEBUG under msg.
// The Enabled check skips the body read when DEBUG is off.
func (rc *RegistrationClient) logBody(ctx context.Context, msg string, resp *client.Response) {
	if !rc.logger.Enabled(ctx, slog.LevelDebug) {
		return
	}
	body, _ := resp.Bytes()
	rc.logger.Debug(msg, "status", resp.StatusCode, "body", logSnippet(body))
}

// saveDebugResponse writes a 403 response body to a new temp file when DEBUG is on.
// Each 403 gets its own file, so two workers never write the same file.
func (rc *RegistrationClient) saveDebugResponse(ctx context.Context, body []byte) {
	if !rc.logger.Enabled(ctx, slog.LevelDebug) {
		return
	}
	f, err := os.CreateTemp("", "debug_403_*.html")
	if err == nil {
		_, err = f.Write(body)
		err = errors.Join(err, f.Close())
	}
	if err != nil {
		rc.logger.Warn("failed to save 403 response body", "error", err)
		return
	}
	rc.logger.Debug("saved 403 response body", "file", f.Name())
}
