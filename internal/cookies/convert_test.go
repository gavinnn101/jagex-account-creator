package cookies

import (
	"testing"
	"time"

	"github.com/mafredri/cdp/protocol/network"
	"github.com/sardanioss/httpcloak/client"
)

func TestCDPToHTTP(t *testing.T) {
	tests := []struct {
		name     string
		input    []network.Cookie
		wantLen  int
		validate func(t *testing.T, cookies []client.Cookie)
	}{
		{
			name:    "nil input",
			input:   nil,
			wantLen: 0,
		},
		{
			name:    "empty input",
			input:   []network.Cookie{},
			wantLen: 0,
		},
		{
			name: "basic cookie",
			input: []network.Cookie{
				{
					Name:     "session",
					Value:    "abc123",
					Domain:   ".example.com",
					Path:     "/",
					Expires:  0,
					HTTPOnly: true,
					Secure:   true,
					SameSite: network.CookieSameSiteLax,
				},
			},
			wantLen: 1,
			validate: func(t *testing.T, cookies []client.Cookie) {
				t.Helper()
				c := cookies[0]
				if c.Name != "session" {
					t.Errorf("Name = %q, want %q", c.Name, "session")
				}
				if c.Value != "abc123" {
					t.Errorf("Value = %q, want %q", c.Value, "abc123")
				}
				if c.Domain != ".example.com" {
					t.Errorf("Domain = %q, want %q", c.Domain, ".example.com")
				}
				if c.Path != "/" {
					t.Errorf("Path = %q, want %q", c.Path, "/")
				}
				if !c.HttpOnly {
					t.Error("HttpOnly = false, want true")
				}
				if !c.Secure {
					t.Error("Secure = false, want true")
				}
				if c.SameSite != "Lax" {
					t.Errorf("SameSite = %q, want %q", c.SameSite, "Lax")
				}
				if !c.Expires.IsZero() {
					t.Errorf("Expires = %v, want zero (session cookie)", c.Expires)
				}
			},
		},
		{
			name: "cookie with expiry",
			input: []network.Cookie{
				{
					Name:    "persistent",
					Value:   "val",
					Domain:  "example.com",
					Path:    "/path",
					Expires: 1700000000,
				},
			},
			wantLen: 1,
			validate: func(t *testing.T, cookies []client.Cookie) {
				t.Helper()
				c := cookies[0]
				want := time.Unix(1700000000, 0)
				if !c.Expires.Equal(want) {
					t.Errorf("Expires = %v, want %v", c.Expires, want)
				}
			},
		},
		{
			name: "negative expiry treated as session",
			input: []network.Cookie{
				{
					Name:    "neg",
					Value:   "v",
					Domain:  "example.com",
					Expires: -1,
				},
			},
			wantLen: 1,
			validate: func(t *testing.T, cookies []client.Cookie) {
				t.Helper()
				if !cookies[0].Expires.IsZero() {
					t.Errorf("Expires = %v, want zero", cookies[0].Expires)
				}
			},
		},
		{
			name: "all SameSite values",
			input: []network.Cookie{
				{Name: "strict", Value: "v", Domain: "d", SameSite: network.CookieSameSiteStrict},
				{Name: "lax", Value: "v", Domain: "d", SameSite: network.CookieSameSiteLax},
				{Name: "none", Value: "v", Domain: "d", SameSite: network.CookieSameSiteNone},
				{Name: "notset", Value: "v", Domain: "d", SameSite: network.CookieSameSiteNotSet},
			},
			wantLen: 4,
			validate: func(t *testing.T, cookies []client.Cookie) {
				t.Helper()
				expected := []string{"Strict", "Lax", "None", ""}
				for i, c := range cookies {
					if c.SameSite != expected[i] {
						t.Errorf("cookie %q: SameSite = %q, want %q", c.Name, c.SameSite, expected[i])
					}
				}
			},
		},
		{
			name: "multiple cookies preserves order",
			input: []network.Cookie{
				{Name: "a", Value: "1", Domain: "d"},
				{Name: "b", Value: "2", Domain: "d"},
				{Name: "c", Value: "3", Domain: "d"},
			},
			wantLen: 3,
			validate: func(t *testing.T, cookies []client.Cookie) {
				t.Helper()
				names := []string{"a", "b", "c"}
				for i, c := range cookies {
					if c.Name != names[i] {
						t.Errorf("cookie[%d].Name = %q, want %q", i, c.Name, names[i])
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CDPToHTTP(tt.input)
			if len(got) != tt.wantLen {
				t.Fatalf("len = %d, want %d", len(got), tt.wantLen)
			}
			if tt.validate != nil {
				tt.validate(t, got)
			}
		})
	}
}
