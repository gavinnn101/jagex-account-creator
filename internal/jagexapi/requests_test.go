package jagexapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sardanioss/httpcloak/client"

	"jagex_account_creator/internal/logtest"
)

// --- Payload structure tests (verify methods build correct payloads) ---

func TestInitRegistrationPayload(t *testing.T) {
	var got *client.Request
	rc := newTestRCWithDoFn(t, func(_ context.Context, req *client.Request) (*client.Response, error) {
		got = req
		return mockResp(200, `{}`), nil
	})
	if err := rc.InitRegistration(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if referer := got.Headers["referer"]; !slices.Equal(referer, []string{testLandingURL}) {
		t.Errorf("referer = %v, want %q", referer, testLandingURL)
	}
	body, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if decoded["loginChallenge"] != testLoginChallenge {
		t.Errorf("loginChallenge = %q, want %q", decoded["loginChallenge"], testLoginChallenge)
	}
	if len(decoded) != 1 {
		t.Errorf("payload keys = %v, want loginChallenge only", decoded)
	}
}

// The live endpoint answers MISSING_PARAMETER unless loginChallenge sits at the
// top level, beside data. This test is that regression guard.
func TestSubmitInitialRegistrationPayload(t *testing.T) {
	email := "user@example.com"
	dob := "2000-01-15T00:00:00.000Z"

	var got *client.Request
	rc := newTestRCWithDoFn(t, func(_ context.Context, req *client.Request) (*client.Response, error) {
		got = req
		return mockResp(200, `{}`), nil
	})
	if err := rc.SubmitInitialRegistration(context.Background(), email, dob); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if referer := got.Headers["referer"]; !slices.Equal(referer, []string{testLandingURL}) {
		t.Errorf("referer = %v, want %q", referer, testLandingURL)
	}
	body, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	var challenge string
	if err := json.Unmarshal(decoded["loginChallenge"], &challenge); err != nil {
		t.Fatalf("no top-level loginChallenge string in body %s: %v", body, err)
	}
	if challenge != testLoginChallenge {
		t.Errorf("loginChallenge = %q, want %q", challenge, testLoginChallenge)
	}

	var data map[string]string
	if err := json.Unmarshal(decoded["data"], &data); err != nil {
		t.Fatalf("unmarshal data from body %s: %v", body, err)
	}
	if data["email"] != email {
		t.Errorf("email = %q, want %q", data["email"], email)
	}
	if data["dob"] != dob {
		t.Errorf("dob = %q, want %q", data["dob"], dob)
	}
}

func TestSubmitVerificationCodePayload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rc, reqs := newScriptedRC(t, []*client.Response{
			mockResp(200, `{}`),
			mockRespHeaders(200, `{"state":"REGULAR_REGISTRATION_FINALIZING"}`, map[string][]string{"etag": {`W/"c1-a"`}}),
			mockRespHeaders(304, "", map[string][]string{"etag": {`W/"c1-a"`}}),
		})
		if err := rc.SubmitVerificationCode(t.Context(), "ABC123"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got := (*reqs)[0]

		body, err := io.ReadAll(got.Body)
		if err != nil {
			t.Fatalf("reading body: %v", err)
		}
		var decoded map[string]string
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if decoded["code"] != "ABC123" {
			t.Errorf("code = %q, want %q (body %s)", decoded["code"], "ABC123", body)
		}
	})
}

func TestSubmitPasswordPayload(t *testing.T) {
	var got *client.Request
	rc := newTestRCWithDoFn(t, func(_ context.Context, req *client.Request) (*client.Response, error) {
		got = req
		return mockResp(200, `{"state":"COMPLETED"}`), nil
	})
	if err := rc.SubmitPassword(context.Background(), "TestUser", "SecurePass!23"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	body, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if decoded["displayName"] != "TestUser" {
		t.Errorf("displayName = %v, want %q", decoded["displayName"], "TestUser")
	}
	if decoded["password"] != "SecurePass!23" {
		t.Errorf("password = %v, want %q", decoded["password"], "SecurePass!23")
	}
	if decoded["receiveEmails"] != false {
		t.Errorf("receiveEmails = %v, want false", decoded["receiveEmails"])
	}
	if decoded["thirdPartyConsent"] != false {
		t.Errorf("thirdPartyConsent = %v, want false", decoded["thirdPartyConsent"])
	}
}

func TestSetupTOTPPayload(t *testing.T) {
	var got *client.Request
	rc := newTestRCWithDoFn(t, func(_ context.Context, req *client.Request) (*client.Response, error) {
		got = req
		return mockResp(200, `{"id":"totp-123","secret":"JBSWY3DPEHPK3PXP"}`), nil
	})
	if _, err := rc.SetupTOTP(context.Background(), "user@example.com"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	body, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if decoded["label"] != "user@example.com" {
		t.Errorf("label = %q, want %q (body %s)", decoded["label"], "user@example.com", body)
	}
}

func TestActivateTOTPPayload(t *testing.T) {
	var got *client.Request
	rc := newTestRCWithDoFn(t, func(_ context.Context, req *client.Request) (*client.Response, error) {
		got = req
		return mockResp(200, `{"backupCodes":["ABC123"]}`), nil
	})
	if _, err := rc.ActivateTOTP(context.Background(), "abc-123-def", "123456"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	body, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if decoded["verificationCode"] != "123456" {
		t.Errorf("verificationCode = %q, want %q (body %s)", decoded["verificationCode"], "123456", body)
	}
}

func TestRegistrationCalls_RequestShape(t *testing.T) {
	for _, tt := range registrationCalls() {
		t.Run(tt.name, func(t *testing.T) {
			rc, got := tt.scriptedRC(t, 200)
			if err := tt.call(t, t.Context(), rc); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// The GETs of /current after the first request have their own tests.
			want := 1
			if tt.then != nil {
				want += len(tt.then())
			}
			if len(*got) != want {
				t.Fatalf("requests = %d, want %d", len(*got), want)
			}
			assertRequest(t, "request", (*got)[0], tt.method, tt.url, tt.referer, tt.shape)
		})
	}
}

// --- saveDebugResponse tests ---

// setTempDir points os.TempDir at dir for the test, on Unix and on Windows.
func setTempDir(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, dir)
	}
}

// debug403Files returns the 403 body files that saveDebugResponse wrote in dir.
func debug403Files(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "debug_403_*.html"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return files
}

func TestSaveDebugResponse(t *testing.T) {
	infoLogger := func(w io.Writer) *slog.Logger {
		return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	body := []byte("<html>cloudflare 403</html>")
	for _, tt := range []struct {
		name       string
		logger     func(io.Writer) *slog.Logger
		missingDir bool   // the temp dir does not exist
		wantFiles  int    // a written file holds the body, and the log line ends with its name
		wantLog    string // the start of the only log line, or "" for no log line
	}{
		{"debug_on", logtest.New, false, 1, `level=DEBUG msg="saved 403 response body" file=`},
		{"debug_off", infoLogger, false, 0, ""},
		{"missing_temp_dir", logtest.New, true, 0, `level=WARN msg="failed to save 403 response body" error=`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tempDir := dir
			if tt.missingDir {
				tempDir = filepath.Join(dir, "missing")
			}
			setTempDir(t, tempDir)
			var logs bytes.Buffer
			rc := newTestRC(newTestClient(t))
			rc.logger = tt.logger(&logs)

			rc.saveDebugResponse(context.Background(), body)

			files := debug403Files(t, dir)
			if len(files) != tt.wantFiles {
				t.Fatalf("files = %v, want %d", files, tt.wantFiles)
			}
			got := logs.String()
			switch {
			case tt.wantFiles == 1:
				if saved, err := os.ReadFile(files[0]); err != nil || !bytes.Equal(saved, body) {
					t.Errorf("file content = %q, %v, want %q", saved, err, body)
				}
				if want := tt.wantLog + files[0] + "\n"; got != want {
					t.Errorf("log = %q, want %q", got, want)
				}
			case tt.wantLog == "":
				if got != "" {
					t.Errorf("log = %q, want none", got)
				}
			case !strings.HasPrefix(got, tt.wantLog) || strings.Count(got, "\n") != 1:
				t.Errorf("log = %q, want one line that starts with %q", got, tt.wantLog)
			}
		})
	}
}

func TestSaveDebugResponse_TwoCallsWriteTwoFiles(t *testing.T) {
	dir := t.TempDir()
	setTempDir(t, dir)
	rc := newTestRC(newTestClient(t))
	rc.logger = logtest.New(io.Discard)

	rc.saveDebugResponse(context.Background(), []byte("first"))
	rc.saveDebugResponse(context.Background(), []byte("second"))

	files := debug403Files(t, dir)
	bodies := make([]string, 0, len(files))
	for _, file := range files {
		saved, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		bodies = append(bodies, string(saved))
	}
	slices.Sort(bodies)
	if want := []string{"first", "second"}; !slices.Equal(bodies, want) {
		t.Errorf("file bodies = %q, want %q", bodies, want)
	}
}

// --- SubmitInitialRegistration response handling ---

func TestSubmitInitialRegistration_Response_SavesOnly403(t *testing.T) {
	for _, tt := range []struct {
		status    int
		wantSaved bool
	}{
		{403, true},
		{400, false},
	} {
		t.Run(fmt.Sprintf("status_%d", tt.status), func(t *testing.T) {
			dir := t.TempDir()
			setTempDir(t, dir)
			rc := newTestRCWithDoFn(t, func(_ context.Context, _ *client.Request) (*client.Response, error) {
				return mockResp(tt.status, "<html>blocked</html>"), nil
			})
			var logs bytes.Buffer
			rc.logger = debugLogger(&logs)

			if err := rc.SubmitInitialRegistration(context.Background(), "test@example.com", "2000-01-01T00:00:00.000Z"); !errors.Is(err, ErrRegistrationFailed) {
				t.Fatalf("error = %v, want wrapping ErrRegistrationFailed", err)
			}

			files := debug403Files(t, dir)
			if tt.wantSaved != (len(files) == 1) || len(files) > 1 {
				t.Fatalf("saved files = %v, want saved %v", files, tt.wantSaved)
			}
			if tt.wantSaved {
				// The body stays readable after describeResponse read it for the error.
				if saved, err := os.ReadFile(files[0]); err != nil || string(saved) != "<html>blocked</html>" {
					t.Errorf("saved body = %q, %v, want the response body", saved, err)
				}
			}
			records := debugRecords(t, &logs)
			gotSaved := slices.ContainsFunc(records, func(rec map[string]any) bool { return rec["msg"] == "saved 403 response body" })
			if gotSaved != tt.wantSaved {
				t.Errorf("log lines = %v, want the saved line %v", records, tt.wantSaved)
			}
		})
	}
}

func TestSubmitInitialRegistration_EntersVerifyScreen(t *testing.T) {
	rc, _ := newScriptedRC(t, []*client.Response{mockResp(200, `{"state":"EMAIL_CODE_VERIFICATION"}`)})
	if err := rc.SubmitInitialRegistration(context.Background(), "test@example.com", "2000-01-01T00:00:00.000Z"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := screenURL(screenVerify); rc.page != want {
		t.Errorf("page = %q, want %q", rc.page, want)
	}

	rc, _ = newScriptedRC(t, []*client.Response{mockResp(400, `{}`)})
	if err := rc.SubmitInitialRegistration(context.Background(), "test@example.com", "2000-01-01T00:00:00.000Z"); err == nil {
		t.Fatal("status 400: error = nil, want a failure")
	}
	if rc.page != testLandingURL {
		t.Errorf("page after a failure = %q, want the landing page", rc.page)
	}
}

// --- Screens and timed requests ---

func TestEnterScreen(t *testing.T) {
	rc := newTestRC(newTestClient(t))
	const base = "https://account.jagex.com/en-GB/login/"
	// Each screen is beside the landing page, also when the client is on another screen.
	for _, tt := range []struct{ screen, want string }{
		{screenVerify, base + "registration-verify?login_challenge=" + testLoginChallenge},
		{screenAccountName, base + "registration-complete/account-name?login_challenge=" + testLoginChallenge},
		{screenPassword, base + "registration-complete/password?login_challenge=" + testLoginChallenge},
	} {
		rc.enterScreen(tt.screen)
		if rc.page != tt.want {
			t.Errorf("enterScreen(%q) page = %q, want %q", tt.screen, rc.page, tt.want)
		}
	}
	if rc.landing.String() != testLandingURL {
		t.Errorf("landing = %q, want it unchanged %q", rc.landing, testLandingURL)
	}
}

// timedRC returns a registration client whose transport gives the answers in
// order. It also returns the requests and the clock time of each request.
func timedRC(t *testing.T, answers []*client.Response) (*RegistrationClient, *[]*client.Request, *[]time.Time) {
	t.Helper()
	var got []*client.Request
	var at []time.Time
	rc := newTestRCWithDoFn(t, func(_ context.Context, req *client.Request) (*client.Response, error) {
		got = append(got, req)
		at = append(at, time.Now())
		if len(got) > len(answers) {
			t.Errorf("request %d (%s %s) has no scripted answer", len(got), req.Method, req.URL)
			return nil, errors.New("no scripted answer")
		}
		return answers[len(got)-1], nil
	})
	return rc, &got, &at
}

// assertGaps fails t unless the requests were sent at the given gaps after the first.
func assertGaps(t *testing.T, at []time.Time, want ...time.Duration) {
	t.Helper()
	if len(at) != len(want)+1 {
		t.Fatalf("requests = %d, want %d", len(at), len(want)+1)
	}
	for i, w := range want {
		if gap := at[i+1].Sub(at[0]); gap != w {
			t.Errorf("request %d sent %v after the first, want %v", i+2, gap, w)
		}
	}
}

// --- SubmitVerificationCode response handling ---

func TestSubmitVerificationCode_PollsUntilFinalizing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const etagA, etagB = `W/"b8-a"`, `W/"c1-b"`
		rc, got, at := timedRC(t, []*client.Response{
			mockResp(200, `{"state":"AWAITING_USER_CREATING"}`),
			mockRespHeaders(200, `{"state":"AWAITING_USER_CREATING"}`, map[string][]string{"etag": {etagA}}),
			mockRespHeaders(200, finalizingAnswer, map[string][]string{"etag": {etagB}}),
			mockRespHeaders(304, "", map[string][]string{"etag": {etagB}}),
		})
		rc.enterScreen(screenVerify)

		if err := rc.SubmitVerificationCode(t.Context(), "ABC123"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// The poll reads at once after the POST, then 2s after the answer. The screen GET follows the last poll at once.
		assertGaps(t, *at, 0, 2*time.Second, 2*time.Second)
		verify, accountName := screenURL(screenVerify), screenURL(screenAccountName)
		assertRequest(t, "code POST", (*got)[0], "POST", statesURL, verify, chromeLocaleXHRPost())
		assertRequest(t, "poll 1", (*got)[1], "GET", currentRegistrationURL, verify, chromeCurrentGet())
		assertRequest(t, "poll 2", (*got)[2], "GET", currentRegistrationURL, verify, chromeCurrentGet().with("if-none-match", etagA))
		assertRequest(t, "account-name GET", (*got)[3], "GET", currentRegistrationURL, accountName, chromeCurrentGet().with("if-none-match", etagB))
		if rc.page != accountName || rc.current.state != stateFinalizing || rc.current.etag != etagB {
			t.Errorf("page %q, current %+v, want %q and the FINALIZING answer with %s", rc.page, rc.current, accountName, etagB)
		}
	})
}

func TestSubmitVerificationCode_PollFailure(t *testing.T) {
	rc, got := newScriptedRC(t, []*client.Response{mockResp(200, `{}`), mockResp(403, `{}`)})
	err := rc.SubmitVerificationCode(context.Background(), "ABC123")
	if want := "reading the registration state: account registration failed: status 403, body: 2 bytes"; !errors.Is(err, ErrRegistrationFailed) || err.Error() != want {
		t.Errorf("error = %v, want %q wrapping ErrRegistrationFailed", err, want)
	}
	if len(*got) != 2 || rc.page != testLandingURL {
		t.Errorf("requests = %d, page %q, want 2 and no screen change", len(*got), rc.page)
	}
}

// --- ValidateDisplayName response handling ---

func TestValidateDisplayName_EntersPasswordScreen(t *testing.T) {
	const etag = `W/"c1-a"`
	rc, got := newScriptedRC(t, []*client.Response{
		mockResp(200, `{"valid":true,"errors":[]}`),
		mockRespHeaders(304, "", map[string][]string{"etag": {etag}}),
	})
	rc.enterScreen(screenAccountName)
	rc.current.state, rc.current.etag = stateFinalizing, etag

	if err := rc.ValidateDisplayName(context.Background(), "TestUser"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*got) != 2 {
		t.Fatalf("requests = %d, want 2", len(*got))
	}
	assertRequest(t, "validate POST", (*got)[0], "POST", validateDisplayName, screenURL(screenAccountName), chromeLocaleXHRPost())
	assertRequest(t, "password GET", (*got)[1], "GET", currentRegistrationURL, screenURL(screenPassword), chromeCurrentGet().with("if-none-match", etag))
	if rc.page != screenURL(screenPassword) || rc.current.state != stateFinalizing {
		t.Errorf("page %q, state %q, want the password screen and the state kept by the 304", rc.page, rc.current.state)
	}
}

func TestValidateDisplayName_Rejected(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"not valid", `{"valid":false,"errors":["NAME_TAKEN"]}`, `validating display name: account registration failed: the name is not valid: ["NAME_TAKEN"]`},
		{"no valid field", `{}`, `validating display name: account registration failed: the name is not valid: `},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc, got := newScriptedRC(t, []*client.Response{mockResp(200, tt.body)})
			rc.enterScreen(screenAccountName)

			err := rc.ValidateDisplayName(context.Background(), "TestUser")

			if !errors.Is(err, ErrRegistrationFailed) || err.Error() != tt.want {
				t.Errorf("error = %v, want %q wrapping ErrRegistrationFailed", err, tt.want)
			}
			if len(*got) != 1 || rc.page != screenURL(screenAccountName) {
				t.Errorf("requests = %d, page %q, want 1 and the account-name screen", len(*got), rc.page)
			}
		})
	}
}

// --- SubmitPassword response handling ---

func TestSubmitPassword_Response_States(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string // "" means no error
	}{
		{"creating", creatingAnswer, ""},
		{"completed", completedAnswer, ""},
		{"creating with data not an object", `{"state":"AWAITING_USER_DETAILS_CREATING","data":"text"}`, ""},
		{"finalizing", finalizingAnswer, `submitting password: account registration failed: registration state "REGULAR_REGISTRATION_FINALIZING", reason: `},
		{"rollback", `{"state":"REGULAR_REGISTRATION_FINALIZING_ROLLBACK"}`, `submitting password: account registration failed: registration state "REGULAR_REGISTRATION_FINALIZING_ROLLBACK", reason: `},
		{"rollback with a string reason", `{"state":"REGULAR_REGISTRATION_FINALIZING_ROLLBACK","data":{"reason":"TEST_REASON"}}`, `submitting password: account registration failed: registration state "REGULAR_REGISTRATION_FINALIZING_ROLLBACK", reason: "TEST_REASON"`},
		{"rollback with an array reason", `{"state":"REGULAR_REGISTRATION_FINALIZING_ROLLBACK","data":{"reason":["A","B"]}}`, `submitting password: account registration failed: registration state "REGULAR_REGISTRATION_FINALIZING_ROLLBACK", reason: ["A","B"]`},
		{"rollback with data not an object", `{"state":"REGULAR_REGISTRATION_FINALIZING_ROLLBACK","data":"text"}`, `submitting password: account registration failed: registration state "REGULAR_REGISTRATION_FINALIZING_ROLLBACK", reason: `},
		{"no state field", `{"other":"field"}`, `submitting password: account registration failed: registration state "", reason: `},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc, got := newScriptedRC(t, []*client.Response{mockResp(200, tt.body)})
			err := rc.SubmitPassword(context.Background(), "TestUser", "SecureP@ss!23")
			if len(*got) != 1 {
				t.Errorf("requests = %d, want the POST only", len(*got))
			}
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrRegistrationFailed) || !Refused(err) || err.Error() != tt.wantErr {
				t.Errorf("error = %v, want the refusal %q wrapping ErrRegistrationFailed", err, tt.wantErr)
			}
		})
	}
}

// --- WaitForAccountCreation response handling ---

func TestWaitForAccountCreation_Response_CompletedImmediately(t *testing.T) {
	// A fake-clock deadline turns a poll that never ends into a fast failure.
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		rc := newTestRCWithDoFn(t, func(_ context.Context, _ *client.Request) (*client.Response, error) {
			calls.Add(1)
			return mockResp(200, completedAnswer), nil
		})
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		if err := rc.WaitForAccountCreation(ctx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := calls.Load(); got != 1 {
			t.Errorf("calls = %d, want 1", got)
		}
	})
}

func TestWaitForAccountCreation_Response_PollsEveryTwoSeconds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rc, _, at := timedRC(t, []*client.Response{
			mockResp(200, creatingAnswer),
			mockResp(200, creatingAnswer),
			mockResp(200, completedAnswer),
		})
		if err := rc.WaitForAccountCreation(t.Context()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertGaps(t, *at, 2*time.Second, 4*time.Second)
	})
}

func TestWaitForAccountCreation_Response_SendsETagOfLastFullAnswer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const etagA, etagB = `W/"62-a"`, `W/"62-b"`
		rc, got := newScriptedRC(t, []*client.Response{
			mockRespHeaders(200, creatingAnswer, map[string][]string{"etag": {etagA}}),
			mockRespHeaders(304, "", map[string][]string{"etag": {etagA}}),
			mockRespHeaders(200, creatingAnswer, map[string][]string{"etag": {etagB}}),
			mockResp(200, completedAnswer),
		})
		rc.enterScreen(screenPassword)
		if err := rc.WaitForAccountCreation(t.Context()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(*got) != 4 {
			t.Fatalf("calls = %d, want 4", len(*got))
		}
		want := []chromeShape{
			chromeCurrentGet(),
			chromeCurrentGet().with("if-none-match", etagA),
			chromeCurrentGet().with("if-none-match", etagA),
			chromeCurrentGet().with("if-none-match", etagB),
		}
		for i, w := range want {
			assertRequest(t, fmt.Sprintf("poll %d", i+1), (*got)[i], "GET", currentRegistrationURL, screenURL(screenPassword), w)
		}
	})
}

func TestWaitForAccountCreation_FirstPollSendsTheScreenETag(t *testing.T) {
	const etag = `W/"c1-a"`
	rc, got := newScriptedRC(t, []*client.Response{
		mockResp(200, `{"valid":true,"errors":[]}`),
		mockRespHeaders(200, finalizingAnswer, map[string][]string{"etag": {etag}}),
		mockResp(200, creatingAnswer),
		mockResp(200, completedAnswer),
	})
	rc.enterScreen(screenAccountName)
	ctx := context.Background()
	if err := rc.ValidateDisplayName(ctx, "TestUser"); err != nil {
		t.Fatalf("ValidateDisplayName: %v", err)
	}
	if err := rc.SubmitPassword(ctx, "TestUser", "SecureP@ss!23"); err != nil {
		t.Fatalf("SubmitPassword: %v", err)
	}
	if err := rc.WaitForAccountCreation(ctx); err != nil {
		t.Fatalf("WaitForAccountCreation: %v", err)
	}
	if len(*got) != 4 {
		t.Fatalf("requests = %d, want 4", len(*got))
	}
	password := screenURL(screenPassword)
	assertRequest(t, "password GET", (*got)[1], "GET", currentRegistrationURL, password, chromeCurrentGet())
	assertRequest(t, "password POST", (*got)[2], "POST", statesURL, password, chromeLocaleXHRPost())
	assertRequest(t, "creation poll", (*got)[3], "GET", currentRegistrationURL, password, chromeCurrentGet().with("if-none-match", etag))
}

func TestWaitForAccountCreation_Response_FailsAtOnce(t *testing.T) {
	// A fake-clock deadline turns a poll that never ends into a fast failure.
	for _, state := range []string{"REGULAR_REGISTRATION_FINALIZING_ROLLBACK", stateFinalizing} {
		t.Run(state, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int32
				rc := newTestRCWithDoFn(t, func(_ context.Context, _ *client.Request) (*client.Response, error) {
					calls.Add(1)
					return mockResp(200, `{"state":"`+state+`"}`), nil
				})
				ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
				defer cancel()
				err := rc.WaitForAccountCreation(ctx)
				if want := `waiting for account creation: account registration failed: registration state "` + state + `"`; !errors.Is(err, ErrRegistrationFailed) || err.Error() != want {
					t.Errorf("error = %v, want %q wrapping ErrRegistrationFailed", err, want)
				}
				if got := calls.Load(); got != 1 {
					t.Errorf("calls = %d, want 1", got)
				}
			})
		})
	}
}

func TestWaitForAccountCreation_Response_ContextDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		rc := newTestRCWithDoFn(t, func(_ context.Context, _ *client.Request) (*client.Response, error) {
			calls.Add(1)
			return mockResp(200, creatingAnswer), nil
		})
		// The deadline falls between the second poll and the third.
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		err := rc.WaitForAccountCreation(ctx)
		if want := "polling the registration state: context deadline exceeded"; !errors.Is(err, context.DeadlineExceeded) || err.Error() != want {
			t.Errorf("error = %v, want %q wrapping context.DeadlineExceeded", err, want)
		}
		if got := calls.Load(); got != 2 {
			t.Errorf("calls = %d, want 2", got)
		}
	})
}

// --- SetupTOTP response handling ---

func TestSetupTOTP_Response_Success(t *testing.T) {
	rc := newTestRCWithDoFn(t, func(_ context.Context, _ *client.Request) (*client.Response, error) {
		return mockResp(200, `{"id":"totp-abc","secret":"JBSWY3DPEHPK3PXP"}`), nil
	})
	result, err := rc.SetupTOTP(context.Background(), "user@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ID != "totp-abc" {
		t.Errorf("ID = %q, want %q", result.ID, "totp-abc")
	}
	if result.Secret != "JBSWY3DPEHPK3PXP" {
		t.Errorf("Secret = %q, want %q", result.Secret, "JBSWY3DPEHPK3PXP")
	}
}

// --- ActivateTOTP response handling ---

func TestActivateTOTP_Response_Success(t *testing.T) {
	rc := newTestRCWithDoFn(t, func(_ context.Context, _ *client.Request) (*client.Response, error) {
		return mockResp(200, `{"backupCodes":["ABC123","DEF456","GHI789"]}`), nil
	})
	codes, err := rc.ActivateTOTP(context.Background(), "totp-abc", "123456")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(codes) != 3 {
		t.Fatalf("backup codes count = %d, want 3", len(codes))
	}
	if codes[0] != "ABC123" {
		t.Errorf("codes[0] = %q, want %q", codes[0], "ABC123")
	}
}

func TestActivateTOTP_Response_EmptyBackupCodes(t *testing.T) {
	rc := newTestRCWithDoFn(t, func(_ context.Context, _ *client.Request) (*client.Response, error) {
		return mockResp(200, `{"backupCodes":[]}`), nil
	})
	codes, err := rc.ActivateTOTP(context.Background(), "totp-abc", "123456")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(codes) != 0 {
		t.Errorf("backup codes count = %d, want 0", len(codes))
	}
}

// --- The registration and TOTP calls ---

// registrationAnswer is a success answer that every call of registrationCalls decodes.
const registrationAnswer = `{"state":"COMPLETED","valid":true,"id":"totp-abc","secret":"JBSWY3DPEHPK3PXP","backupCodes":["ABC123"]}`

// The bodies of /current as the server answers them.
const (
	finalizingAnswer = `{"state":"REGULAR_REGISTRATION_FINALIZING"}`
	creatingAnswer   = `{"state":"AWAITING_USER_DETAILS_CREATING"}`
	completedAnswer  = `{"state":"COMPLETED"}`
)

// notJSON is a body that no decode accepts.
const notJSON = "not json"

// notJSONError returns the text of the decode error of notJSON.
func notJSONError() string {
	var v any
	return json.Unmarshal([]byte(notJSON), &v).Error()
}

// registrationCall is one registration or TOTP call and the first request that it sends.
type registrationCall struct {
	name                 string
	screen               string // the screen that the call starts on. Empty means the landing page
	call                 func(t *testing.T, ctx context.Context, rc *RegistrationClient) error
	method, url, referer string
	shape                chromeShape
	errPrefix            string                    // the wrap of a status error, a transport error and a 429
	decodeErr            string                    // the text before the JSON error when every answer is notJSON. Empty means the call decodes no body
	decodeWraps          bool                      // whether the decode error wraps ErrRegistrationFailed
	then                 func() []*client.Response // the answers of the GETs of /current after the first request
}

// scriptedRC returns a registration client on the call's screen. The first
// request gets status and registrationAnswer, and the GETs after it get the call's answers.
func (c registrationCall) scriptedRC(t *testing.T, status int) (*RegistrationClient, *[]*client.Request) {
	t.Helper()
	answers := []*client.Response{mockResp(status, registrationAnswer)}
	if c.then != nil {
		answers = append(answers, c.then()...)
	}
	rc, got := newScriptedRC(t, answers)
	if c.screen != "" {
		rc.enterScreen(c.screen)
	}
	return rc, got
}

// registrationCalls returns the calls that fail on a 4xx or 5xx status and pass any other.
func registrationCalls() []registrationCall {
	const (
		manageURL  = "https://account.jagex.com/en-GB/manage"
		regularURL = "https://account.jagex.com/login/api/registrations/regular"
	)
	return []registrationCall{
		{
			name: "InitRegistration", call: func(_ *testing.T, ctx context.Context, rc *RegistrationClient) error { return rc.InitRegistration(ctx) },
			method: "POST", url: regularURL, referer: testLandingURL, shape: chromeXHRPost(), errPrefix: "initializing registration: ",
		},
		{
			name: "SubmitInitialRegistration", call: func(_ *testing.T, ctx context.Context, rc *RegistrationClient) error {
				return rc.SubmitInitialRegistration(ctx, "test@example.com", "2000-01-01T00:00:00.000Z")
			},
			method: "POST", url: regularURL + "/continue", referer: testLandingURL, shape: chromeLocaleXHRPost(), errPrefix: "submitting initial registration: ",
		},
		{
			name: "SubmitVerificationCode", screen: screenVerify,
			call: func(_ *testing.T, ctx context.Context, rc *RegistrationClient) error {
				return rc.SubmitVerificationCode(ctx, "ABC123")
			},
			method: "POST", url: regularURL + "/states", referer: screenURL(screenVerify), shape: chromeLocaleXHRPost(), errPrefix: "submitting verification code: ",
			// The POST decodes no body, so the decode error is the first poll's.
			decodeErr: "account registration failed: decoding the registration state: ", decodeWraps: true,
			then: func() []*client.Response {
				return []*client.Response{
					mockRespHeaders(200, finalizingAnswer, map[string][]string{"etag": {`W/"c1-a"`}}),
					mockRespHeaders(304, "", map[string][]string{"etag": {`W/"c1-a"`}}),
				}
			},
		},
		{
			name: "ValidateDisplayName", screen: screenAccountName,
			call: func(_ *testing.T, ctx context.Context, rc *RegistrationClient) error {
				return rc.ValidateDisplayName(ctx, "TestUser")
			},
			method: "POST", url: regularURL + "/displayNames/validate", referer: screenURL(screenAccountName), shape: chromeLocaleXHRPost(), errPrefix: "validating display name: ",
			decodeErr: "validating display name: account registration failed: decoding the validation answer: ", decodeWraps: true,
			then: func() []*client.Response { return []*client.Response{mockResp(200, finalizingAnswer)} },
		},
		{
			name: "SubmitPassword", screen: screenPassword,
			call: func(_ *testing.T, ctx context.Context, rc *RegistrationClient) error {
				return rc.SubmitPassword(ctx, "TestUser", "SecureP@ss!23")
			},
			method: "POST", url: regularURL + "/states", referer: screenURL(screenPassword), shape: chromeLocaleXHRPost(), errPrefix: "submitting password: ",
			// A 2xx answer that does not decode is no refusal.
			decodeErr: "decoding password response: ",
		},
		{
			name: "WaitForAccountCreation", screen: screenPassword,
			call: func(_ *testing.T, ctx context.Context, rc *RegistrationClient) error {
				return rc.WaitForAccountCreation(ctx)
			},
			method: "GET", url: regularURL + "/current", referer: screenURL(screenPassword), shape: chromeCurrentGet(), errPrefix: "reading the registration state: ",
			decodeErr: "account registration failed: decoding the registration state: ", decodeWraps: true,
		},
		{
			name: "SetupTOTP", call: func(t *testing.T, ctx context.Context, rc *RegistrationClient) error {
				t.Helper()
				result, err := rc.SetupTOTP(ctx, "user@example.com")
				if err != nil && result != nil {
					t.Errorf("result = %+v with error %v, want nil", result, err)
				}
				return err
			},
			method: "POST", url: "https://account.jagex.com/manage/api/users/mfa/totp", referer: manageURL,
			shape: chromeLocaleXHRPost().with("accept", "application/json"), errPrefix: "setting up TOTP: ",
			decodeErr: "account registration failed: decoding TOTP setup response: ", decodeWraps: true,
		},
		{
			name: "ActivateTOTP", call: func(t *testing.T, ctx context.Context, rc *RegistrationClient) error {
				t.Helper()
				codes, err := rc.ActivateTOTP(ctx, "totp-abc", "123456")
				if err != nil && codes != nil {
					t.Errorf("codes = %v with error %v, want nil", codes, err)
				}
				return err
			},
			method: "POST", url: "https://account.jagex.com/manage/api/users/mfa/totp/totp-abc/activations", referer: manageURL,
			shape: chromeLocaleXHRPost().with("accept", "application/json"), errPrefix: "activating TOTP: ",
			decodeErr: "decoding TOTP activation response: ",
		},
	}
}

func TestRegistrationCalls_StatusCheck(t *testing.T) {
	// 399 is the highest status that passes, and 400 the lowest that fails.
	wantFailure := fmt.Sprintf("account registration failed: status 400, body: %d bytes", len(registrationAnswer))
	for _, tt := range registrationCalls() {
		t.Run(tt.name, func(t *testing.T) {
			rc, _ := tt.scriptedRC(t, 399)
			if err := tt.call(t, t.Context(), rc); err != nil {
				t.Errorf("status 399: error = %v, want nil", err)
			}

			rc, _ = newScriptedRC(t, []*client.Response{mockResp(400, registrationAnswer)})
			err := tt.call(t, t.Context(), rc)
			if want := tt.errPrefix + wantFailure; !errors.Is(err, ErrRegistrationFailed) || !Refused(err) || err.Error() != want {
				t.Errorf("status 400: error = %v, want the refusal %q wrapping ErrRegistrationFailed", err, want)
			}
		})
	}
}

func TestRegistrationCalls_POSTSentOnceOn5xx(t *testing.T) {
	// The scripted client fails a second send, so a resent POST fails the test.
	wantTail := fmt.Sprintf(": server error: status 502, body: %d bytes", len(registrationAnswer))
	for _, tt := range registrationCalls() {
		if tt.method != "POST" {
			continue
		}
		t.Run(tt.name, func(t *testing.T) {
			rc, got := newScriptedRC(t, []*client.Response{mockResp(502, registrationAnswer)})
			if tt.screen != "" {
				rc.enterScreen(tt.screen)
			}
			err := tt.call(t, t.Context(), rc)
			want := tt.errPrefix + "executing request to " + tt.url + wantTail
			if err == nil || err.Error() != want || errors.Is(err, ErrRegistrationFailed) || Refused(err) {
				t.Errorf("error = %v, want %q with no sentinel", err, want)
			}
			if len(*got) != 1 {
				t.Errorf("requests = %d, want 1", len(*got))
			}
		})
	}
}

// cancelAtRequest returns a context and a client whose transport gives the
// answers in order. Request n cancels the context and fails, and a request after it fails the test.
func cancelAtRequest(t *testing.T, n int, answers []*client.Response) (context.Context, *Client) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	calls := 0
	c := newTestClientWithDoFn(t, func(_ context.Context, req *client.Request) (*client.Response, error) {
		calls++
		switch {
		case calls == n:
			cancel()
			return nil, errors.New("connection reset")
		case calls > n:
			t.Errorf("request %d (%s %s) after the cancel", calls, req.Method, req.URL)
			return nil, errors.New("request after the cancel")
		}
		return answers[calls-1], nil
	})
	return ctx, c
}

func TestRegistrationCalls_TransportError(t *testing.T) {
	for _, tt := range registrationCalls() {
		t.Run(tt.name, func(t *testing.T) {
			ctx, c := cancelAtRequest(t, 1, nil)
			err := tt.call(t, ctx, newTestRC(c))
			if want := tt.errPrefix + "executing request to " + tt.url + ": context canceled"; !errors.Is(err, context.Canceled) || Refused(err) || err.Error() != want {
				t.Errorf("error = %v, want %q wrapping context.Canceled", err, want)
			}
		})
	}
}

func TestRegistrationCalls_RateLimited(t *testing.T) {
	// The scripted client fails a retry, so the 429 must end the call at once.
	wantTail := fmt.Sprintf(": rate limited: status 429, body: %d bytes", len(registrationAnswer))
	for _, tt := range registrationCalls() {
		t.Run(tt.name, func(t *testing.T) {
			rc, _ := newScriptedRC(t, []*client.Response{mockResp(429, registrationAnswer)})
			err := tt.call(t, t.Context(), rc)
			want := tt.errPrefix + "executing request to " + tt.url + wantTail
			if !errors.Is(err, ErrRateLimited) || errors.Is(err, ErrRegistrationFailed) || !Refused(err) || err.Error() != want {
				t.Errorf("error = %v, want the refusal %q wrapping ErrRateLimited and not ErrRegistrationFailed", err, want)
			}
		})
	}
}

func TestRegistrationCalls_DecodeError(t *testing.T) {
	for _, tt := range registrationCalls() {
		t.Run(tt.name, func(t *testing.T) {
			rc := newTestRCWithDoFn(t, func(context.Context, *client.Request) (*client.Response, error) {
				return mockResp(200, notJSON), nil
			})
			err := tt.call(t, t.Context(), rc)
			if tt.decodeErr == "" {
				if err != nil {
					t.Errorf("error = %v, want nil from a call that decodes no body", err)
				}
				return
			}
			if want := tt.decodeErr + notJSONError(); err == nil || err.Error() != want || errors.Is(err, ErrRegistrationFailed) != tt.decodeWraps || Refused(err) != tt.decodeWraps {
				t.Errorf("error = %v, want %q, wrapping ErrRegistrationFailed and refused %v", err, want, tt.decodeWraps)
			}
		})
	}
}

func TestSuccessBodyDebugLine(t *testing.T) {
	body := strings.Repeat("x", 512) + strings.Repeat("y", 100)
	ctx := context.Background()
	tests := []struct {
		msg  string
		call func(rc *RegistrationClient) error
	}{
		{"registration init response", func(rc *RegistrationClient) error { return rc.InitRegistration(ctx) }},
		{"initial registration response", func(rc *RegistrationClient) error {
			return rc.SubmitInitialRegistration(ctx, "test@example.com", "2000-01-01T00:00:00.000Z")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.msg, func(t *testing.T) {
			rc, _ := newScriptedRC(t, []*client.Response{mockResp(200, body)})
			var logs bytes.Buffer
			rc.logger = debugLogger(&logs)

			if err := tt.call(rc); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			var lines []map[string]any
			for _, rec := range debugRecords(t, &logs) {
				if rec["msg"] == tt.msg {
					lines = append(lines, rec)
				}
			}
			if len(lines) != 1 || lines[0]["level"] != "DEBUG" || lines[0]["status"] != float64(200) || lines[0]["body"] != body[:512] {
				t.Errorf("%q lines = %v, want one DEBUG line with status 200 and the first 512 bytes", tt.msg, lines)
			}
		})
	}
}

// --- Login ---

const (
	testConsentPage = "https://account.jagex.com/consent?consent_challenge=cc1"
	testVerifierURL = "https://account.jagex.com/oauth2/auth?client_id=player-portal&consent_verifier=cv1"
	testCodePage    = "https://account.jagex.com/en-GB/manage/oauth2/code?code=code1&scope=openid+offline&state=st1"
)

// redirectResp returns a redirect answer with the given Location.
func redirectResp(status int, location string) *client.Response {
	return mockRespHeaders(status, "", map[string][]string{"location": {location}})
}

// loginAnswers returns the five answers of a login that succeeds. The first
// Location is relative, so the login must resolve it.
func loginAnswers() []*client.Response {
	return []*client.Response{
		redirectResp(302, "/oauth2/auth?client_id=player-portal&login_verifier=lv1"),
		redirectResp(302, testConsentPage),
		mockResp(200, `{"redirectTo":"`+testVerifierURL+`","accountId":"a1"}`),
		redirectResp(303, testCodePage),
		mockResp(204, ""),
	}
}

func TestLogin_RequestShape(t *testing.T) {
	rc, got := newScriptedRC(t, loginAnswers())
	if err := rc.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}

	const completedPage = "https://account.jagex.com/en-GB/login/registration-completed"
	want := []struct {
		method, url, referer string
		shape                chromeShape
		body                 map[string]string
	}{
		{"GET", "https://account.jagex.com/en-GB/login/registration-continue", completedPage, chromeNavigation(true), nil},
		{"GET", "https://account.jagex.com/oauth2/auth?client_id=player-portal&login_verifier=lv1", completedPage, chromeRedirect(true), nil},
		{"POST", "https://account.jagex.com/api/auth/consent", testConsentPage, chromeXHRPost(), map[string]string{"consentChallenge": "cc1"}},
		{"GET", testVerifierURL, testConsentPage, chromeNavigation(false), nil},
		{"POST", "https://account.jagex.com/manage/api/oauth/token?lang=en-GB", testCodePage, chromeLocaleXHRPost(), map[string]string{"code": "code1", "scope": "openid offline", "state": "st1"}},
	}
	if len(*got) != len(want) {
		t.Fatalf("requests = %d, want %d", len(*got), len(want))
	}
	for i, w := range want {
		req := (*got)[i]
		assertRequest(t, fmt.Sprintf("request %d", i), req, w.method, w.url, w.referer, w.shape)
		if w.body == nil {
			continue
		}
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("request %d: reading body: %v", i, err)
		}
		var body map[string]string
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("request %d: body %s: %v", i, raw, err)
		}
		if !maps.Equal(body, w.body) {
			t.Errorf("request %d body = %v, want %v", i, body, w.body)
		}
	}
}

func TestLogin_HopFailures(t *testing.T) {
	const (
		codePath     = "https://account.jagex.com/en-GB/manage/oauth2/code"
		verifierBody = `{"redirectTo":"` + testVerifierURL + `"}`
	)
	tests := []struct {
		name    string
		hop     int // index of the answer that the case replaces
		answer  *client.Response
		want    error
		wantErr string
	}{
		{"continue answers 200", 0, mockResp(200, "<html></html>"), ErrRegistrationFailed, "continuing registration: account registration failed: status 200, body: 13 bytes, want a redirect"},
		{"continue has no login_verifier", 0, redirectResp(302, "/oauth2/auth?client_id=player-portal"), ErrRegistrationFailed, "continuing registration: account registration failed: /oauth2/auth carries no login_verifier"},
		{"auth has no consent_challenge", 1, redirectResp(302, "/consent"), ErrRegistrationFailed, "following the login verifier: account registration failed: /consent carries no consent_challenge"},
		{"auth has an empty consent_challenge", 1, redirectResp(302, "/consent?consent_challenge="), ErrRegistrationFailed, "following the login verifier: account registration failed: /consent carries no consent_challenge"},
		{"auth answers 429", 1, mockResp(429, ""), ErrRateLimited, "following the login verifier: executing request to /oauth2/auth: rate limited: status 429, body: 0 bytes"},
		{"consent answers 403", 2, mockResp(403, `{}`), ErrRegistrationFailed, "accepting consent: account registration failed: status 403, body: 2 bytes"},
		{"consent answers 300", 2, mockResp(300, verifierBody), ErrRegistrationFailed, fmt.Sprintf("accepting consent: account registration failed: status 300, body: %d bytes", len(verifierBody))},
		{"consent body does not decode", 2, mockResp(200, notJSON), ErrRegistrationFailed, "accepting consent: account registration failed: decoding consent response: " + notJSONError()},
		{"consent has no redirectTo", 2, mockResp(200, `{"accountId":"a1"}`), ErrRegistrationFailed, "accepting consent: account registration failed: consent response has no redirectTo"},
		{"redirectTo does not parse", 2, mockResp(200, `{"redirectTo":"%zz"}`), ErrRegistrationFailed, "accepting consent: account registration failed: the redirect target does not parse"},
		{"redirectTo has no consent_verifier", 2, mockResp(200, `{"redirectTo":"/oauth2/auth?client_id=player-portal"}`), ErrRegistrationFailed, "accepting consent: account registration failed: /oauth2/auth carries no consent_verifier"},
		{"code page has no code", 3, redirectResp(303, codePath+"?scope=openid&state=st1"), ErrRegistrationFailed, "following the consent verifier: account registration failed: /en-GB/manage/oauth2/code carries no code"},
		{"code page has no state", 3, redirectResp(303, codePath+"?code=code1&scope=openid"), ErrRegistrationFailed, "following the consent verifier: account registration failed: /en-GB/manage/oauth2/code carries no state"},
		{"code page has no scope", 3, redirectResp(303, codePath+"?code=code1&state=st1"), ErrRegistrationFailed, "following the consent verifier: account registration failed: /en-GB/manage/oauth2/code carries no scope"},
		{"token answers 400", 4, mockResp(400, ""), ErrRegistrationFailed, "exchanging the code: account registration failed: status 400, body: 0 bytes"},
		{"token answers 300", 4, mockResp(300, ""), ErrRegistrationFailed, "exchanging the code: account registration failed: status 300, body: 0 bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			answers := loginAnswers()
			answers[tt.hop] = tt.answer
			rc, got := newScriptedRC(t, answers[:tt.hop+1])

			err := rc.Login(context.Background())

			if !errors.Is(err, tt.want) || err.Error() != tt.wantErr {
				t.Errorf("error = %v, want %q wrapping %v", err, tt.wantErr, tt.want)
			}
			if len(*got) != tt.hop+1 {
				t.Errorf("requests = %d, want %d", len(*got), tt.hop+1)
			}
		})
	}
}

func TestLogin_TransportError(t *testing.T) {
	// The messages name the path only, so the one-time verifier of hop 1 stays out.
	tests := []struct {
		hop     int
		wantErr string
	}{
		{0, "continuing registration: executing request to /en-GB/login/registration-continue: context canceled"},
		{1, "following the login verifier: executing request to /oauth2/auth: context canceled"},
		{2, "accepting consent: executing request to https://account.jagex.com/api/auth/consent: context canceled"},
		{4, "exchanging the code: executing request to https://account.jagex.com/manage/api/oauth/token?lang=en-GB: context canceled"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("hop_%d", tt.hop), func(t *testing.T) {
			ctx, c := cancelAtRequest(t, tt.hop+1, loginAnswers())

			err := newTestRC(c).Login(ctx)

			if !errors.Is(err, context.Canceled) || err.Error() != tt.wantErr {
				t.Errorf("error = %v, want %q wrapping context.Canceled", err, tt.wantErr)
			}
		})
	}
}

// --- LoginLink and StartRegistration ---

const (
	testLoginLink     = "https://account.jagex.com/oauth2/auth?client_id=player-portal&flow=registration&state=st1"
	testChallengePage = "https://account.jagex.com/?login_challenge=" + testLoginChallenge
	testManagePage    = "https://account.jagex.com/en-GB/manage?flow=registration"
)

// startAnswers returns the two answers of a start chain that succeeds. The
// last Location is relative, so the chain must resolve it.
func startAnswers() []*client.Response {
	return []*client.Response{
		redirectResp(302, testChallengePage),
		redirectResp(302, "/en-GB/login/registration-start?login_challenge="+testLoginChallenge),
	}
}

// loginLinkURL returns testLoginLink as a URL.
func loginLinkURL(t *testing.T) *url.URL {
	t.Helper()
	u, err := url.Parse(testLoginLink)
	if err != nil {
		t.Fatalf("parsing %q: %v", testLoginLink, err)
	}
	return u
}

func TestLoginLink_RequestShape(t *testing.T) {
	c, got := newScriptedClient(t, []*client.Response{mockResp(200, `{"loginLink":"`+testLoginLink+`"}`)})

	link, err := LoginLink(context.Background(), c, c.logger)
	if err != nil {
		t.Fatalf("LoginLink: %v", err)
	}

	if len(*got) != 1 {
		t.Fatalf("requests = %d, want 1", len(*got))
	}
	assertRequest(t, "request 0", (*got)[0], "GET", "https://account.jagex.com/manage/api/oauth/login?flow=registration&locale=en-GB", testManagePage, chromeLoginLink())
	if link.String() != testLoginLink {
		t.Errorf("link = %q, want %q", link, testLoginLink)
	}
}

func TestLoginLink_Failures(t *testing.T) {
	const (
		loginLinkBody = `{"loginLink":"` + testLoginLink + `"}`
		prefix        = "getting the login link: account registration failed: "
	)
	tests := []struct {
		name    string
		answer  *client.Response
		wantErr string
	}{
		{"login answers 403", mockRespHeaders(403, `{}`, map[string][]string{"cf-mitigated": {"challenge"}}), "status 403, cf-mitigated: challenge, body: 2 bytes"},
		{"login answers 300", mockResp(300, loginLinkBody), fmt.Sprintf("status 300, body: %d bytes", len(loginLinkBody))},
		{"login body does not decode", mockResp(200, notJSON), "decoding login link response: " + notJSONError()},
		{"login has no loginLink", mockResp(200, `{"other":"x"}`), "login link response has no loginLink"},
		{"login has an empty loginLink", mockResp(200, `{"loginLink":""}`), "login link response has no loginLink"},
		{"loginLink does not parse", mockResp(200, `{"loginLink":"%zz"}`), "the redirect target does not parse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, got := newScriptedClient(t, []*client.Response{tt.answer})

			link, err := LoginLink(context.Background(), c, c.logger)

			if want := prefix + tt.wantErr; !errors.Is(err, ErrRegistrationFailed) || err.Error() != want {
				t.Errorf("error = %v, want %q wrapping ErrRegistrationFailed", err, want)
			}
			if link != nil {
				t.Errorf("link = %v, want nil", link)
			}
			if len(*got) != 1 {
				t.Errorf("requests = %d, want 1", len(*got))
			}
		})
	}
}

func TestLoginLink_TransportError(t *testing.T) {
	ctx, c := cancelAtRequest(t, 1, nil)

	link, err := LoginLink(ctx, c, c.logger)

	const want = "getting the login link: executing request to https://account.jagex.com/manage/api/oauth/login?flow=registration&locale=en-GB: context canceled"
	if !errors.Is(err, context.Canceled) || err.Error() != want || link != nil {
		t.Errorf("LoginLink = %v, %v, want nil, %q wrapping context.Canceled", link, err, want)
	}
}

func TestStartRegistration_RequestShape(t *testing.T) {
	c, got := newScriptedClient(t, startAnswers())

	rc, err := StartRegistration(context.Background(), c, c.logger, loginLinkURL(t))
	if err != nil {
		t.Fatalf("StartRegistration: %v", err)
	}

	if len(*got) != 2 {
		t.Fatalf("requests = %d, want 2", len(*got))
	}
	assertRequest(t, "request 0", (*got)[0], "GET", testLoginLink, testManagePage, chromeNavigation(false))
	assertRequest(t, "request 1", (*got)[1], "GET", testChallengePage, testManagePage, chromeRedirect(false))
	if rc.client != c || rc.landing.String() != testLandingURL || rc.page != testLandingURL || rc.loginChallenge != testLoginChallenge {
		t.Errorf("client %p, landing %q, page %q, challenge %q, want %p, %q, %q, %q", rc.client, rc.landing, rc.page, rc.loginChallenge, c, testLandingURL, testLandingURL, testLoginChallenge)
	}
}

func TestStartRegistration_HopFailures(t *testing.T) {
	const startPage = "https://account.jagex.com/en-GB/login/registration-start"
	tests := []struct {
		name    string
		hop     int // index of the answer that the case replaces
		answer  *client.Response
		wantErr string
	}{
		{
			"auth answers the challenge", 0, mockRespHeaders(403, "<html></html>", map[string][]string{"cf-mitigated": {"challenge"}}),
			"following the login link: account registration failed: status 403, cf-mitigated: challenge, body: 13 bytes, want a redirect",
		},
		{"auth has no login_challenge", 0, redirectResp(302, "/?flow=registration"), "following the login link: account registration failed: / carries no login_challenge"},
		{"root has no login_challenge", 1, redirectResp(302, startPage), "following the login challenge: account registration failed: /en-GB/login/registration-start carries no login_challenge"},
		{
			"login page on another host", 1, redirectResp(302, "https://evil.example.com/en-GB/login/registration-start?login_challenge=lc1"),
			"following the login challenge: account registration failed: the login page is not on account.jagex.com",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			answers := startAnswers()
			answers[tt.hop] = tt.answer
			c, got := newScriptedClient(t, answers[:tt.hop+1])

			rc, err := StartRegistration(context.Background(), c, c.logger, loginLinkURL(t))

			if !errors.Is(err, ErrRegistrationFailed) || err.Error() != tt.wantErr {
				t.Errorf("error = %v, want %q wrapping ErrRegistrationFailed", err, tt.wantErr)
			}
			if rc != nil {
				t.Errorf("client = %+v, want nil", rc)
			}
			if len(*got) != tt.hop+1 {
				t.Errorf("requests = %d, want %d", len(*got), tt.hop+1)
			}
		})
	}
}

func TestStartRegistration_TransportError(t *testing.T) {
	// The redirect hops name the path only, so the state of the login link stays out.
	tests := []struct {
		hop     int
		wantErr string
	}{
		{0, "following the login link: executing request to /oauth2/auth: context canceled"},
		{1, "following the login challenge: executing request to /: context canceled"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("hop_%d", tt.hop), func(t *testing.T) {
			ctx, c := cancelAtRequest(t, tt.hop+1, startAnswers())

			rc, err := StartRegistration(ctx, c, c.logger, loginLinkURL(t))

			if !errors.Is(err, context.Canceled) || err.Error() != tt.wantErr {
				t.Errorf("error = %v, want %q wrapping context.Canceled", err, tt.wantErr)
			}
			if rc != nil {
				t.Errorf("client = %+v, want nil", rc)
			}
		})
	}
}

func TestFollowRedirect(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		answer  *client.Response
		want    string
		wantErr string
	}{
		{name: "299 is no redirect", answer: redirectResp(299, "/next?k=v"), wantErr: "account registration failed: status 299, body: 0 bytes, want a redirect"},
		{name: "300 redirects", answer: redirectResp(300, "/next?k=v"), want: "https://account.jagex.com/next?k=v"},
		{name: "399 redirects", answer: redirectResp(399, "/next?k=v"), want: "https://account.jagex.com/next?k=v"},
		{name: "400 is no redirect", answer: redirectResp(400, "/next?k=v"), wantErr: "account registration failed: status 400, body: 0 bytes, want a redirect"},
		{name: "no Location", answer: mockResp(302, ""), wantErr: "account registration failed: status 302, body: 0 bytes, no Location"},
		{name: "relative Location", answer: redirectResp(302, "next?k=v"), want: "https://account.jagex.com/en-GB/login/next?k=v"},
		{name: "absolute Location", answer: redirectResp(303, "https://account.jagex.com/other?k=v&x=1"), want: "https://account.jagex.com/other?k=v&x=1"},
		{name: "Location does not parse", answer: redirectResp(302, "%zz"), wantErr: "account registration failed: the redirect target does not parse"},
		{name: "target does not parse", target: "%zz", answer: redirectResp(302, "/next?k=v"), wantErr: "account registration failed: the request URL does not parse"},
		{name: "Location has no key", answer: redirectResp(302, "/next?x=1"), wantErr: "account registration failed: /next carries no k"},
		{name: "Location has an empty key", answer: redirectResp(302, "/next?k="), wantErr: "account registration failed: /next carries no k"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := tt.target
			if target == "" {
				target = registrationContinueURL
			}
			rc, _ := newScriptedRC(t, []*client.Response{tt.answer})

			got, err := rc.followRedirect(context.Background(), navigationShape(false), target, testManagePage, "k")

			if tt.wantErr != "" {
				if !errors.Is(err, ErrRegistrationFailed) || err.Error() != tt.wantErr {
					t.Errorf("error = %v, want %q wrapping ErrRegistrationFailed", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("followRedirect: %v", err)
			}
			if got.String() != tt.want {
				t.Errorf("redirect = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRequestPath(t *testing.T) {
	for _, tt := range []struct{ target, want string }{
		{"https://account.jagex.com/oauth2/auth?login_verifier=lv1", "/oauth2/auth"},
		{"https://account.jagex.com/", "/"},
		{"%zz", ""},
	} {
		if got := requestPath(tt.target); got != tt.want {
			t.Errorf("requestPath(%q) = %q, want %q", tt.target, got, tt.want)
		}
	}
}

func TestRequireStatus(t *testing.T) {
	rc := newTestRC(newTestClient(t))
	tests := []struct {
		name          string
		require       func(context.Context, *client.Response) error
		pass, failing []int
	}{
		{"requireSuccess", rc.requireSuccess, []int{200, 204, 299}, []int{199, 300, 400}},
		{"requireNotError", rc.requireNotError, []int{200, 302, 399}, []int{400, 404, 500}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, status := range tt.pass {
				if err := tt.require(t.Context(), mockResp(status, "{}")); err != nil {
					t.Errorf("status %d: error = %v, want nil", status, err)
				}
			}
			for _, status := range tt.failing {
				err := tt.require(t.Context(), mockResp(status, "{}"))
				if want := fmt.Sprintf("account registration failed: status %d, body: 2 bytes", status); !errors.Is(err, ErrRegistrationFailed) || err.Error() != want {
					t.Errorf("status %d: error = %v, want %q wrapping ErrRegistrationFailed", status, err, want)
				}
			}
		})
	}
}
