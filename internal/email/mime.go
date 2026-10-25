package email

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
)

// decodeMessageBody parses a raw RFC 5322 message and returns the decoded body.
// For multipart messages, it prefers text/html over text/plain since the
// verification code is in the HTML body. Handles Content-Transfer-Encoding:
// base64, quoted-printable, 7bit, 8bit.
// It returns its parse errors. The caller owns the fallback to the raw bytes.
func decodeMessageBody(raw []byte) ([]byte, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parsing message: %w", err)
	}

	contentType := msg.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "text/plain"
	}

	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, fmt.Errorf("parsing Content-Type: %w", err)
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		return decodeMultipart(msg.Body, params["boundary"])
	}

	return decodeTransferEncoding(msg.Body, msg.Header.Get("Content-Transfer-Encoding"))
}

// decodeMultipart walks a multipart message recursively. It returns the first
// non-empty nested multipart body, else the text/html part, else the text/plain part.
func decodeMultipart(body io.Reader, boundary string) ([]byte, error) {
	if boundary == "" {
		return readBody(body)
	}

	reader := multipart.NewReader(body, boundary)
	var parts textParts

	for {
		part, err := reader.NextPart()
		if err != nil {
			break
		}

		partType := part.Header.Get("Content-Type")
		mediaType, partParams, _ := mime.ParseMediaType(partType)

		// Handle nested multipart (e.g., multipart/alternative inside multipart/mixed).
		if strings.HasPrefix(mediaType, "multipart/") {
			nested, err := decodeMultipart(part, partParams["boundary"])
			if err == nil && len(nested) > 0 {
				return nested, nil
			}
			continue
		}

		decoded, err := decodeTransferEncoding(part, part.Header.Get("Content-Transfer-Encoding"))
		if err != nil {
			continue
		}

		parts.add(mediaType, decoded)
	}

	return parts.best()
}

// textParts holds the text candidates of one multipart level.
type textParts struct {
	html, plain []byte
}

// add keeps the last text/html part and the first text/plain part.
func (t *textParts) add(mediaType string, decoded []byte) {
	if strings.HasPrefix(mediaType, "text/html") {
		t.html = decoded
	} else if strings.HasPrefix(mediaType, "text/plain") && t.plain == nil {
		t.plain = decoded
	}
}

// best returns the html part, else the plain part.
func (t *textParts) best() ([]byte, error) {
	if t.html != nil {
		return t.html, nil
	}
	if t.plain != nil {
		return t.plain, nil
	}
	return nil, errors.New("no text part found in multipart message")
}

// decodeTransferEncoding decodes the body according to its Content-Transfer-Encoding.
func decodeTransferEncoding(r io.Reader, encoding string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		return readBody(base64.NewDecoder(base64.StdEncoding, r))
	case "quoted-printable":
		return readBody(quotedprintable.NewReader(r))
	default: // 7bit, 8bit, binary, or empty
		return readBody(r)
	}
}

// readBody reads r to the end. On an error it returns the bytes read so far.
func readBody(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return data, fmt.Errorf("reading message body: %w", err)
	}
	return data, nil
}
