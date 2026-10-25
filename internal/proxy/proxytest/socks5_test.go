package proxytest

import (
	"io"
	"net"
	"reflect"
	"testing"
	"time"
)

const (
	greeting = "\x05\x01\x02"                     // VER NMETHODS METHODS: username/password only
	auth     = "\x01\x01u\x02pw"                  // VER ULEN UNAME PLEN PASSWD
	connect  = "\x05\x01\x00\x03\x04h.tx\x01\xbb" // CONNECT h.tx:443
	methodOK = "\x05\x02"
	authOK   = "\x01\x00"
)

// reply is the fake's answer to a request.
func reply(code byte) string {
	return string([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0})
}

// dial connects to the fake and sends raw.
func dial(t *testing.T, f *SOCKS5, raw string) *net.TCPConn {
	t.Helper()
	c, err := net.Dial("tcp", f.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	tc, ok := c.(*net.TCPConn)
	if !ok {
		t.Fatalf("conn is a %T, want *net.TCPConn", c)
	}
	if err := tc.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tc, raw); err != nil {
		t.Fatal(err)
	}
	return tc
}

// expect reads len(want) bytes and compares them with want.
func expect(t *testing.T, c net.Conn, want string) {
	t.Helper()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("reading %q: %v", want, err)
	}
	if string(got) != want {
		t.Fatalf("read %q, want %q", got, want)
	}
}

// rest half-closes c and returns what the fake sends until it closes, so the
// fake's handler has returned when rest returns.
func rest(t *testing.T, c *net.TCPConn) string {
	t.Helper()
	if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("reading to the close: %v", err)
	}
	return string(b)
}

func checkRecords(t *testing.T, f *SOCKS5, want []Record) {
	t.Helper()
	n, recs := f.Snapshot()
	if n != 1 {
		t.Errorf("accepted = %d, want 1", n)
	}
	if !reflect.DeepEqual(recs, want) {
		t.Errorf("records = %+v, want %+v", recs, want)
	}
}

func TestConnectEchoes(t *testing.T) {
	f := NewSOCKS5(t, Config{})
	c := dial(t, f, greeting+auth+connect+"ping")
	expect(t, c, methodOK+authOK+reply(0)+"ping")
	if got := rest(t, c); got != "" {
		t.Errorf("after the echo = %q, want nothing", got)
	}
	checkRecords(t, f, []Record{{Methods: []byte{2}, User: "u", Pass: "pw", Cmd: 1, Atyp: 3, Host: "h.tx", Port: 443}})
}

func TestNoAuthSkipsCredentials(t *testing.T) {
	f := NewSOCKS5(t, Config{NoAuth: true})
	c := dial(t, f, "\x05\x01\x00"+connect+"ping")
	expect(t, c, "\x05\x00"+reply(0)+"ping")
	if got := rest(t, c); got != "" {
		t.Errorf("after the echo = %q, want nothing", got)
	}
	checkRecords(t, f, []Record{{Methods: []byte{0}, Cmd: 1, Atyp: 3, Host: "h.tx", Port: 443}})
}

func TestRequestAddressTypes(t *testing.T) {
	tests := []struct {
		name    string
		request string
		want    Record
	}{
		{
			name:    "UDP ASSOCIATE with IPv4",
			request: "\x05\x03\x00\x01\x01\x02\x03\x04\x1f\x90",
			want:    Record{Cmd: 3, Atyp: 1, Host: "1.2.3.4", Port: 8080},
		},
		{
			name:    "CONNECT with IPv6",
			request: "\x05\x01\x00\x04" + "\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01" + "\x01\xbb",
			want:    Record{Cmd: 1, Atyp: 4, Host: "::1", Port: 443},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewSOCKS5(t, Config{Reply: 7})
			c := dial(t, f, greeting+auth+tt.request)
			if got, want := rest(t, c), methodOK+authOK+reply(7); got != want {
				t.Errorf("replies = %q, want %q", got, want)
			}
			want := tt.want
			want.Methods, want.User, want.Pass = []byte{2}, "u", "pw"
			checkRecords(t, f, []Record{want})
		})
	}
}

func TestRefusedRequestIsRecordedWhenServingHTTP(t *testing.T) {
	f := NewSOCKS5(t, Config{Reply: 5, ServeHTTP: true})
	c := dial(t, f, greeting+auth+connect)
	if got, want := rest(t, c), methodOK+authOK+reply(5); got != want {
		t.Errorf("replies = %q, want %q", got, want)
	}
	checkRecords(t, f, []Record{{Methods: []byte{2}, User: "u", Pass: "pw", Cmd: 1, Atyp: 3, Host: "h.tx", Port: 443}})
}

func TestServeHTTP(t *testing.T) {
	f := NewSOCKS5(t, Config{ServeHTTP: true})
	c := dial(t, f, greeting+auth+connect+"GET /x?y=1 HTTP/1.1\r\nHost: h.tx\r\n\r\n")
	const response = "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK"
	if got, want := rest(t, c), methodOK+authOK+reply(0)+response; got != want {
		t.Errorf("replies = %q, want %q", got, want)
	}
	checkRecords(t, f, []Record{{Methods: []byte{2}, User: "u", Pass: "pw", Cmd: 1, Atyp: 3, Host: "h.tx", Port: 443, RequestURI: "/x?y=1"}})
}

func TestAuthRejected(t *testing.T) {
	f := NewSOCKS5(t, Config{AuthStatus: 1})
	c := dial(t, f, greeting+auth+connect)
	if got, want := rest(t, c), methodOK+"\x01\x01"; got != want {
		t.Errorf("replies = %q, want %q", got, want)
	}
	checkRecords(t, f, []Record{{Methods: []byte{2}, User: "u", Pass: "pw"}})
}

func TestBadInputIsNotRecorded(t *testing.T) {
	tests := []struct {
		name      string
		cfg       Config
		raw       string
		wantReply string
	}{
		{name: "method version", raw: "\x04\x01\x02"},
		{name: "truncated methods", raw: "\x05\x02\x00"},
		{name: "auth version", raw: greeting + "\x02\x01u\x01p", wantReply: methodOK},
		{name: "truncated auth", raw: greeting + "\x01\x01u", wantReply: methodOK},
		{name: "address type", raw: greeting + auth + "\x05\x01\x00\x09\x00\x00", wantReply: methodOK + authOK},
		{name: "truncated request", raw: greeting + auth + "\x05\x01\x00\x03\x04h", wantReply: methodOK + authOK},
		{
			name:      "malformed HTTP request",
			cfg:       Config{ServeHTTP: true},
			raw:       greeting + auth + connect + "not http\r\n\r\n",
			wantReply: methodOK + authOK + reply(0),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewSOCKS5(t, tt.cfg)
			c := dial(t, f, tt.raw)
			if got := rest(t, c); got != tt.wantReply {
				t.Errorf("replies = %q, want %q", got, tt.wantReply)
			}
			checkRecords(t, f, nil)
		})
	}
}
