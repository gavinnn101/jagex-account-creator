package browser

import (
	"context"
	"io"
	"net"
	"testing"
	"testing/synctest"

	"github.com/mafredri/cdp/rpcc"
)

func TestCloseNilConn(t *testing.T) {
	c := &Client{}
	if err := c.Close(); err != nil {
		t.Fatalf("Close with nil conn: %v", err)
	}
}

func TestCloseIdempotent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clientSide, serverSide := net.Pipe()
		t.Cleanup(func() { _ = serverSide.Close() })
		conn, err := rpcc.DialContext(t.Context(), "pipe", rpcc.WithDialer(func(context.Context, string) (io.ReadWriteCloser, error) {
			return clientSide, nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		c := &Client{conn: conn}
		// rpcc's second close returns ErrConnClosing. Client.Close hides it.
		for i := range 2 {
			if err := c.Close(); err != nil {
				t.Errorf("Close call %d: %v", i+1, err)
			}
		}
	})
}

func TestWsURLToHTTP(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "Chrome shape with a path",
			in:   "ws://127.0.0.1:9222/devtools/browser/abc-123",
			want: "http://127.0.0.1:9222",
		},
		{
			name: "no path",
			in:   "ws://127.0.0.1:9222",
			want: "http://127.0.0.1:9222",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wsURLToHTTP(tt.in)
			if got != tt.want {
				t.Errorf("wsURLToHTTP(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
