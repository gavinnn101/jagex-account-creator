package email

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// htmlBody is the HTML snippet containing a verification code, as Jagex sends it.
const htmlBody = `<html><body><span data-testid="registration-started-verification-code">ABC123</span></body></html>`

func TestDecodeMessageBody_PlainText(t *testing.T) {
	raw := "Content-Type: text/plain\r\n\r\nHello, world!"
	decoded, err := decodeMessageBody([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(decoded) != "Hello, world!" {
		t.Errorf("got %q, want %q", decoded, "Hello, world!")
	}
}

func TestDecodeMessageBody_NoContentType(t *testing.T) {
	// No Content-Type header should default to text/plain and return body as-is.
	raw := "Subject: test\r\n\r\nplain body"
	decoded, err := decodeMessageBody([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(decoded) != "plain body" {
		t.Errorf("got %q, want %q", decoded, "plain body")
	}
}

func TestDecodeMessageBody_QuotedPrintable(t *testing.T) {
	// Encode the HTML body as quoted-printable (= at end of long lines, =XX for special chars).
	qpBody := "Content-Type: text/html\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\n" +
		"<html><body><span data-testid=3D\"registration-started-verification-cod=\r\n" +
		"e\">ABC123</span></body></html>"
	decoded, err := decodeMessageBody([]byte(qpBody))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(decoded), "ABC123") {
		t.Errorf("decoded body does not contain verification code: %s", decoded)
	}
	// The regex should match the decoded body.
	matches := verificationCodeRe.FindSubmatch(decoded)
	if len(matches) < 2 || string(matches[1]) != "ABC123" {
		t.Errorf("regex did not match decoded QP body, matches=%v", matches)
	}
}

func TestDecodeMessageBody_Base64(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte(htmlBody))
	raw := "Content-Type: text/html\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		encoded
	decoded, err := decodeMessageBody([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(decoded) != htmlBody {
		t.Errorf("got %q, want %q", decoded, htmlBody)
	}
	matches := verificationCodeRe.FindSubmatch(decoded)
	if len(matches) < 2 || string(matches[1]) != "ABC123" {
		t.Errorf("regex did not match decoded base64 body")
	}
}

func TestDecodeMessageBody_7Bit(t *testing.T) {
	raw := "Content-Type: text/html\r\n" +
		"Content-Transfer-Encoding: 7bit\r\n\r\n" +
		htmlBody
	decoded, err := decodeMessageBody([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(decoded) != htmlBody {
		t.Errorf("got %q, want %q", decoded, htmlBody)
	}
}

func TestDecodeMessageBody_MultipartAlternative(t *testing.T) {
	boundary := "----=_Part_123"
	textPart := "Plain text version"
	raw := fmt.Sprintf(
		"Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n"+
			"--%s\r\n"+
			"Content-Type: text/plain\r\n"+
			"Content-Transfer-Encoding: 7bit\r\n\r\n"+
			"%s\r\n"+
			"--%s\r\n"+
			"Content-Type: text/html\r\n"+
			"Content-Transfer-Encoding: 7bit\r\n\r\n"+
			"%s\r\n"+
			"--%s--\r\n",
		boundary, boundary, textPart, boundary, htmlBody, boundary,
	)
	decoded, err := decodeMessageBody([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should prefer text/html over text/plain.
	if !strings.Contains(string(decoded), "ABC123") {
		t.Errorf("decoded multipart body does not contain verification code: %s", decoded)
	}
}

func TestDecodeMessageBody_MultipartWithBase64HTML(t *testing.T) {
	boundary := "boundary42"
	encodedHTML := base64.StdEncoding.EncodeToString([]byte(htmlBody))
	raw := fmt.Sprintf(
		"Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n"+
			"--%s\r\n"+
			"Content-Type: text/plain\r\n\r\n"+
			"plain text\r\n"+
			"--%s\r\n"+
			"Content-Type: text/html\r\n"+
			"Content-Transfer-Encoding: base64\r\n\r\n"+
			"%s\r\n"+
			"--%s--\r\n",
		boundary, boundary, boundary, encodedHTML, boundary,
	)
	decoded, err := decodeMessageBody([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	matches := verificationCodeRe.FindSubmatch(decoded)
	if len(matches) < 2 || string(matches[1]) != "ABC123" {
		t.Errorf("regex did not match base64-encoded HTML in multipart, decoded=%s", decoded)
	}
}

func TestDecodeMessageBody_MultipartWithQPHTML(t *testing.T) {
	boundary := "boundaryQP"
	qpHTML := "<html><body><span data-testid=3D\"registration-started-verification-cod=\r\ne\">XYZ789</span></body></html>"
	raw := fmt.Sprintf(
		"Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n"+
			"--%s\r\n"+
			"Content-Type: text/plain\r\n\r\n"+
			"plain\r\n"+
			"--%s\r\n"+
			"Content-Type: text/html\r\n"+
			"Content-Transfer-Encoding: quoted-printable\r\n\r\n"+
			"%s\r\n"+
			"--%s--\r\n",
		boundary, boundary, boundary, qpHTML, boundary,
	)
	decoded, err := decodeMessageBody([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	matches := verificationCodeRe.FindSubmatch(decoded)
	if len(matches) < 2 || string(matches[1]) != "XYZ789" {
		t.Errorf("regex did not match QP-encoded HTML in multipart, decoded=%s", decoded)
	}
}

func TestDecodeMessageBody_NestedMultipart(t *testing.T) {
	innerBoundary := "inner"
	outerBoundary := "outer"
	raw := fmt.Sprintf(
		"Content-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n"+
			"--%s\r\n"+
			"Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n"+
			"--%s\r\n"+
			"Content-Type: text/plain\r\n\r\n"+
			"text\r\n"+
			"--%s\r\n"+
			"Content-Type: text/html\r\n\r\n"+
			"%s\r\n"+
			"--%s--\r\n"+
			"--%s--\r\n",
		outerBoundary, outerBoundary, innerBoundary,
		innerBoundary, innerBoundary, htmlBody, innerBoundary, outerBoundary,
	)
	decoded, err := decodeMessageBody([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(decoded), "ABC123") {
		t.Errorf("nested multipart decode failed: %s", decoded)
	}
}

func TestDecodeMessageBody_EmptyNestedMultipart(t *testing.T) {
	// A nested multipart that decodes to nothing must not hide a later text part.
	raw := "Content-Type: multipart/mixed; boundary=\"outer\"\r\n\r\n" +
		"--outer\r\n" +
		"Content-Type: multipart/alternative; boundary=\"inner\"\r\n\r\n" +
		"--inner\r\n" +
		"Content-Type: text/html\r\n\r\n" +
		"\r\n" +
		"--inner--\r\n" +
		"--outer\r\n" +
		"Content-Type: text/plain\r\n\r\n" +
		"fallback text\r\n" +
		"--outer--\r\n"
	decoded, err := decodeMessageBody([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(decoded) != "fallback text" {
		t.Errorf("got %q, want %q", decoded, "fallback text")
	}
}

func TestDecodeMessageBody_MultipartTextOnly(t *testing.T) {
	// Multipart with only text/plain (no text/html) should return the text part.
	boundary := "textonly"
	raw := fmt.Sprintf(
		"Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n"+
			"--%s\r\n"+
			"Content-Type: text/plain\r\n\r\n"+
			"fallback text\r\n"+
			"--%s--\r\n",
		boundary, boundary, boundary,
	)
	decoded, err := decodeMessageBody([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(decoded) != "fallback text" {
		t.Errorf("got %q, want %q", decoded, "fallback text")
	}
}

func TestDecodeMessageBody_ParseErrors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"not an RFC 5322 message", "not a valid email at all\x00\x01\x02"},
		// mime.ParseMediaType accepts "%%%invalid" but rejects a type with no subtype.
		{"Content-Type with no subtype", "Content-Type: text/\r\n\r\nbody"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decoded, err := decodeMessageBody([]byte(tt.raw))
			if err == nil || decoded != nil {
				t.Errorf("decodeMessageBody = (%q, %v), want (nil, an error)", decoded, err)
			}
		})
	}
}

func TestDecodeTransferEncoding_Unknown(t *testing.T) {
	// Unknown encoding should pass through as-is.
	r := strings.NewReader("raw data")
	decoded, err := decodeTransferEncoding(r, "x-unknown")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(decoded) != "raw data" {
		t.Errorf("got %q, want %q", decoded, "raw data")
	}
}

func TestDecodeTransferEncoding_EmptyEncoding(t *testing.T) {
	r := strings.NewReader("raw data")
	decoded, err := decodeTransferEncoding(r, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(decoded) != "raw data" {
		t.Errorf("got %q, want %q", decoded, "raw data")
	}
}

func TestDecodeTransferEncoding_CaseInsensitive(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("hello"))
	r := strings.NewReader(encoded)
	decoded, err := decodeTransferEncoding(r, "BASE64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(decoded) != "hello" {
		t.Errorf("got %q, want %q", decoded, "hello")
	}
}

func TestTextParts(t *testing.T) {
	type part struct{ mediaType, body string }
	tests := []struct {
		name  string
		parts []part
		want  string // empty means best returns an error
	}{
		{"html then html keeps the last", []part{{"text/html", "h1"}, {"text/html", "h2"}}, "h2"},
		{"plain then plain keeps the first", []part{{"text/plain", "p1"}, {"text/plain", "p2"}}, "p1"},
		{"html wins over an earlier plain", []part{{"text/plain", "p"}, {"text/html; charset=utf-8", "h"}}, "h"},
		{"html wins over a later plain", []part{{"text/html", "h"}, {"text/plain", "p"}}, "h"},
		{"plain only", []part{{"text/plain", "p"}}, "p"},
		{"other types are ignored", []part{{"image/png", "img"}, {"text/csv", "csv"}}, ""},
		{"no parts", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tp textParts
			for _, p := range tt.parts {
				tp.add(p.mediaType, []byte(p.body))
			}
			got, err := tp.best()
			if tt.want == "" {
				if err == nil {
					t.Fatalf("best() = %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("best(): %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("best() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDecodeMessageBody_MultipartNoTextParts(t *testing.T) {
	// Multipart with only non-text parts should return an error.
	boundary := "notext"
	raw := fmt.Sprintf(
		"Content-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n"+
			"--%s\r\n"+
			"Content-Type: image/png\r\n\r\n"+
			"PNG data\r\n"+
			"--%s--\r\n",
		boundary, boundary, boundary,
	)
	_, err := decodeMessageBody([]byte(raw))
	if err == nil {
		t.Fatal("expected error for multipart with no text parts")
	}
}
