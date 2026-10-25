package jagexapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log/slog"
	"maps"
	"math/big"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sardanioss/httpcloak/client"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// wireRequest is one request as the fake server read it: the method, the path
// and the names of the regular header fields in wire order.
type wireRequest struct {
	method, path string
	names        []string
}

// wireReply is the answer of a route. The server sends no empty location, etag or body.
type wireReply struct {
	status               int
	location, etag, body string
}

// h2Server is a fake upstream proxy. It accepts a CONNECT, ends TLS with a
// self-signed certificate, speaks HTTP/2 and records each request. No byte
// leaves the machine.
type h2Server struct {
	ln     net.Listener
	config *tls.Config
	route  func(method, path string) wireReply

	mu   sync.Mutex
	seen []wireRequest
}

func newH2Server(t *testing.T, route func(method, path string) wireReply) *h2Server {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: accountHost},
		DNSNames:     []string{accountHost},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &h2Server{
		ln:     ln,
		config: &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, NextProtos: []string{"h2"}},
		route:  route,
	}
	t.Cleanup(func() { _ = ln.Close() })
	go s.serve()
	return s
}

func (s *h2Server) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *h2Server) handle(c net.Conn) {
	defer func() { _ = c.Close() }()
	// One byte at a time, so that no TLS byte after the CONNECT head is read here.
	var head []byte
	b := make([]byte, 1)
	for !bytes.HasSuffix(head, []byte("\r\n\r\n")) {
		if _, err := c.Read(b); err != nil {
			return
		}
		head = append(head, b[0])
	}
	if _, err := io.WriteString(c, connectReply); err != nil {
		return
	}
	tc := tls.Server(c, s.config)
	preface := make([]byte, len(http2.ClientPreface))
	if _, err := io.ReadFull(tc, preface); err != nil {
		return
	}
	s.serveH2(tc)
}

// serveH2 reads frames until the connection ends. It answers each request when its last frame arrives.
func (s *h2Server) serveH2(tc *tls.Conn) {
	fr := http2.NewFramer(tc, tc)
	fr.ReadMetaHeaders = hpack.NewDecoder(65536, nil)
	if err := fr.WriteSettings(); err != nil {
		return
	}
	pending := map[uint32]wireRequest{}
	for {
		f, err := fr.ReadFrame()
		if err != nil {
			return
		}
		switch f := f.(type) {
		case *http2.SettingsFrame:
			if !f.IsAck() {
				_ = fr.WriteSettingsAck()
			}
		case *http2.MetaHeadersFrame:
			req := wireRequest{method: f.PseudoValue("method"), path: f.PseudoValue("path")}
			for _, field := range f.RegularFields() {
				req.names = append(req.names, field.Name)
			}
			if f.StreamEnded() {
				s.respond(fr, f.StreamID, req)
			} else {
				pending[f.StreamID] = req
			}
		case *http2.DataFrame:
			if req, ok := pending[f.StreamID]; ok && f.StreamEnded() {
				delete(pending, f.StreamID)
				s.respond(fr, f.StreamID, req)
			}
		}
	}
}

// respond records req and writes the answer of the route.
func (s *h2Server) respond(fr *http2.Framer, stream uint32, req wireRequest) {
	s.mu.Lock()
	s.seen = append(s.seen, req)
	s.mu.Unlock()

	r := s.route(req.method, req.path)
	var block bytes.Buffer
	enc := hpack.NewEncoder(&block)
	_ = enc.WriteField(hpack.HeaderField{Name: ":status", Value: strconv.Itoa(r.status)})
	if r.location != "" {
		_ = enc.WriteField(hpack.HeaderField{Name: "location", Value: r.location})
	}
	if r.etag != "" {
		_ = enc.WriteField(hpack.HeaderField{Name: "etag", Value: r.etag})
	}
	// The server's 304 carries no content-length.
	if r.status != 304 {
		_ = enc.WriteField(hpack.HeaderField{Name: "content-length", Value: strconv.Itoa(len(r.body))})
	}
	_ = fr.WriteHeaders(http2.HeadersFrameParam{StreamID: stream, BlockFragment: block.Bytes(), EndHeaders: true, EndStream: r.body == ""})
	if r.body != "" {
		_ = fr.WriteData(stream, true, []byte(r.body))
	}
}

// requests returns the requests that the server read.
func (s *h2Server) requests() []wireRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.seen)
}

// testFinalizingETag is the ETag of the answer to the code step's poll. The two
// screen GETs and the first creation poll send it.
const testFinalizingETag = `W/"c1-M+z6Kb6gFV41NWhRsi8Q4m05sR4"`

// newJagexRoute returns a route that answers each request of the HTTP phase as the Jagex flow does.
func newJagexRoute() func(method, path string) wireReply {
	var states, reads atomic.Int32
	return func(method, path string) wireReply {
		u, err := url.Parse(path)
		if err != nil {
			return wireReply{status: 400}
		}
		q := u.Query()
		switch method + " " + u.Path {
		case "GET /manage/api/oauth/login":
			return wireReply{status: 200, body: `{"loginLink":"` + testLoginLink + `"}`}
		case "GET /oauth2/auth":
			switch {
			case q.Has("login_verifier"):
				return wireReply{status: 302, location: testConsentPage}
			case q.Has("consent_verifier"):
				return wireReply{status: 303, location: testCodePage}
			default:
				return wireReply{status: 302, location: testChallengePage}
			}
		case "GET /":
			return wireReply{status: 302, location: "/en-GB/login/registration-start?login_challenge=" + testLoginChallenge}
		case "POST /login/api/registrations/regular":
			return wireReply{status: 200, body: `{}`}
		case "POST /login/api/registrations/regular/continue":
			return wireReply{status: 200, body: `{"state":"EMAIL_CODE_VERIFICATION"}`}
		case "POST /login/api/registrations/regular/states":
			return statesReply(states.Add(1))
		case "POST /login/api/registrations/regular/displayNames/validate":
			return wireReply{status: 200, body: `{"valid":true,"errors":[]}`}
		case "GET /login/api/registrations/regular/current":
			return currentReply(reads.Add(1))
		case "GET /en-GB/login/registration-continue":
			return wireReply{status: 302, location: "/oauth2/auth?client_id=player-portal&login_verifier=lv1"}
		case "POST /api/auth/consent":
			return wireReply{status: 200, body: `{"redirectTo":"` + testVerifierURL + `"}`}
		case "POST /manage/api/oauth/token":
			return wireReply{status: 204}
		case "POST /manage/api/users/mfa/totp":
			return wireReply{status: 200, body: `{"id":"totp-abc","secret":"JBSWY3DPEHPK3PXP"}`}
		}
		return wireReply{status: 404}
	}
}

// statesReply answers POST n of /states: the code, then the password.
func statesReply(n int32) wireReply {
	if n == 1 {
		return wireReply{status: 200, body: `{"state":"AWAITING_USER_CREATING"}`}
	}
	return wireReply{status: 200, body: `{"state":"AWAITING_USER_DETAILS_CREATING"}`}
}

// currentReply answers GET n of /current as the measured flow does: the code
// step's poll gets FINALIZING with an ETag, the two screen GETs get a 304, and the creation poll gets COMPLETED.
func currentReply(n int32) wireReply {
	switch n {
	case 1:
		return wireReply{status: 200, etag: testFinalizingETag, body: `{"state":"REGULAR_REGISTRATION_FINALIZING"}`}
	case 2, 3:
		return wireReply{status: 304, etag: testFinalizingETag}
	default:
		return wireReply{status: 200, etag: `W/"155-0skeK7aqoJBSt5BFld28+U0UlYY"`, body: `{"state":"COMPLETED"}`}
	}
}

// wireNames returns the names that a request of want sends on the wire, in
// order. Every request of the flow sends a referer and the jar's cookie.
func wireNames(want chromeShape, method string) []string {
	sent := clientHeaderValues()
	maps.Copy(sent, want.values)
	sent["referer"] = "set"
	sent["cookie"] = "set"
	if method == "POST" {
		sent["content-length"] = "set"
	}
	var names []string
	for _, name := range want.order {
		if _, ok := sent[name]; ok {
			names = append(names, name)
		}
	}
	return names
}

// runWireFlow runs every step of the HTTP phase on c, in the order of the flow.
func runWireFlow(ctx context.Context, t *testing.T, c *Client) {
	t.Helper()
	link, err := LoginLink(ctx, c, c.logger)
	if err != nil {
		t.Fatalf("LoginLink: %v", err)
	}
	rc, err := StartRegistration(ctx, c, c.logger, link)
	if err != nil {
		t.Fatalf("StartRegistration: %v", err)
	}
	if err := rc.InitRegistration(ctx); err != nil {
		t.Fatalf("InitRegistration: %v", err)
	}
	if err := rc.SubmitInitialRegistration(ctx, "user@example.com", "2000-01-01T00:00:00.000Z"); err != nil {
		t.Fatalf("SubmitInitialRegistration: %v", err)
	}
	if err := rc.SubmitVerificationCode(ctx, "ABC123"); err != nil {
		t.Fatalf("SubmitVerificationCode: %v", err)
	}
	if err := rc.ValidateDisplayName(ctx, "TestUser"); err != nil {
		t.Fatalf("ValidateDisplayName: %v", err)
	}
	if err := rc.SubmitPassword(ctx, "TestUser", "SecureP@ss!23"); err != nil {
		t.Fatalf("SubmitPassword: %v", err)
	}
	if err := rc.WaitForAccountCreation(ctx); err != nil {
		t.Fatalf("WaitForAccountCreation: %v", err)
	}
	if err := rc.Login(ctx); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, err := rc.SetupTOTP(ctx, "user@example.com"); err != nil {
		t.Fatalf("SetupTOTP: %v", err)
	}
}

// The client options put httpcloak in TLS-only mode, so each request reaches
// the wire with Chrome's header set and order and nothing that the preset adds.
func TestRequestsOnTheWire_MatchChrome(t *testing.T) {
	srv := newH2Server(t, newJagexRoute())
	opts := append(clientOptions(), client.WithProxy("http://"+srv.ln.Addr().String()), client.WithInsecureSkipVerify())
	inner := client.NewClient(presetName(), opts...)
	inner.EnableCookies()
	c := &Client{inner: inner, logger: slog.New(slog.DiscardHandler), values: headerValues("test", testHints())}
	defer c.Close()
	c.ImportCookies([]client.Cookie{{Name: "cf_clearance", Value: "c1", Domain: ".jagex.com", Path: "/", Secure: true}})

	// The guard: a flow that stalls fails here, not at the test timeout.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	runWireFlow(ctx, t, c)

	want := []struct {
		method, path string
		shape        chromeShape
	}{
		{"GET", "/manage/api/oauth/login", chromeLoginLink()},
		{"GET", "/oauth2/auth", chromeNavigation(false)},
		{"GET", "/", chromeRedirect(false)},
		{"POST", "/login/api/registrations/regular", chromeXHRPost()},
		{"POST", "/login/api/registrations/regular/continue", chromeLocaleXHRPost()},
		{"POST", "/login/api/registrations/regular/states", chromeLocaleXHRPost()},
		{"GET", "/login/api/registrations/regular/current", chromeCurrentGet()},
		{"GET", "/login/api/registrations/regular/current", chromeCurrentGet().with("if-none-match", testFinalizingETag)},
		{"POST", "/login/api/registrations/regular/displayNames/validate", chromeLocaleXHRPost()},
		{"GET", "/login/api/registrations/regular/current", chromeCurrentGet().with("if-none-match", testFinalizingETag)},
		{"POST", "/login/api/registrations/regular/states", chromeLocaleXHRPost()},
		{"GET", "/login/api/registrations/regular/current", chromeCurrentGet().with("if-none-match", testFinalizingETag)},
		{"GET", "/en-GB/login/registration-continue", chromeNavigation(true)},
		{"GET", "/oauth2/auth", chromeRedirect(true)},
		{"POST", "/api/auth/consent", chromeXHRPost()},
		{"GET", "/oauth2/auth", chromeNavigation(false)},
		{"POST", "/manage/api/oauth/token", chromeLocaleXHRPost()},
		{"POST", "/manage/api/users/mfa/totp", chromeLocaleXHRPost()},
	}
	got := srv.requests()
	if len(got) != len(want) {
		t.Fatalf("the server read %d requests, want %d", len(got), len(want))
	}
	for i, w := range want {
		path, _, _ := strings.Cut(got[i].path, "?")
		if got[i].method != w.method || path != w.path {
			t.Errorf("request %d = %s %s, want %s %s", i, got[i].method, path, w.method, w.path)
		}
		if names := wireNames(w.shape, w.method); !slices.Equal(got[i].names, names) {
			t.Errorf("request %d (%s %s) fields = %v, want %v", i, w.method, w.path, got[i].names, names)
		}
	}
}
