package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"reflect"
	"slices"
	"strings"

	"jagex_account_creator/internal/account"
)

// ExportJSONL writes each account that f selects to w as one JSON line, in the
// order of the writes.
func (s *Store) ExportJSONL(ctx context.Context, w io.Writer, f AccountFilter) error {
	return eachLine(ctx, s.db, f, func(line []byte) error {
		if _, err := w.Write(line); err != nil {
			return fmt.Errorf("writing account line: %w", err)
		}
		return nil
	})
}

// eachLine calls fn with the JSON line of each account that f selects, in id order.
func eachLine(ctx context.Context, q dbtx, f AccountFilter, fn func(line []byte) error) error {
	rows, err := queryRows(ctx, q, "accounts", (*accountRow).dest, selectAccountsSQL, f.args()...)
	if err != nil {
		return err
	}
	for i := range rows {
		line, err := rows[i].line()
		if err != nil {
			return err
		}
		if err := fn(line); err != nil {
			return err
		}
	}
	return nil
}

// line encodes x as one JSON line, with HTML escaping off. The keys of the
// extra column follow the known fields.
func (x *accountRow) line() ([]byte, error) {
	sa, err := x.stored()
	if err != nil {
		return nil, err
	}
	line, err := encodeJSON(&sa.Account)
	if err == nil && x.extra.Valid {
		// The keys of the extra object go before the closing brace of the account object.
		line = append(append(line[:len(line)-1], ','), x.extra.String[1:]...)
	}
	return append(line, '\n'), err
}

// importJSONL inserts each line of the JSONL file at path on tx, then checks
// each stored row against its line. found is false when the file does not
// exist. An error names a line by its number only, because a line holds a
// password.
func importJSONL(ctx context.Context, tx *sql.Tx, path string) (imported int, found bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("importing accounts: %w", err)
	}
	known := jsonKeys()
	var lines [][]byte
	// A line keeps its newline. The JSON decoders read it as space.
	for line := range bytes.Lines(data) {
		lines = append(lines, line)
		acct, extra, ok := parseLine(line, known)
		if !ok {
			return 0, false, fmt.Errorf("importing %s line %d: not a JSON account object", path, len(lines))
		}
		if err := insertAccount(ctx, tx, &acct, extra); err != nil {
			return 0, false, fmt.Errorf("importing %s line %d: %w", path, len(lines), err)
		}
	}
	// The table was empty before the import, so row i holds line i.
	// Limitation: a line with an explicit zero value that omitempty drops, such as
	// "proxy":"", or with a created_at that is not UTC fails the check. No producer
	// writes one. It fails toward a refused start, never toward a lost row.
	i := 0
	err = eachLine(ctx, tx, AccountFilter{}, func(row []byte) error {
		if !sameJSON(lines[i], row) {
			return fmt.Errorf("importing %s line %d: the stored row differs from the line", path, i+1)
		}
		i++
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	return len(lines), true, nil
}

// jsonKeys returns the JSON names of the fields of account.Account.
func jsonKeys() map[string]bool {
	fields := jsonFields(reflect.TypeFor[account.Account]())
	keys := make(map[string]bool, len(fields))
	for _, f := range fields {
		keys[f.name] = true
	}
	return keys
}

// jsonField is one field of a struct as its JSON record spells it.
type jsonField struct {
	index     int // the index of the field in its struct
	name      string
	omitEmpty bool // the tag has omitempty or omitzero
}

// jsonFields returns the fields of the struct type t that its JSON record holds, in declaration order.
func jsonFields(t reflect.Type) []jsonField {
	var fields []jsonField
	for field := range t.Fields() {
		name, opts, _ := strings.Cut(field.Tag.Get("json"), ",")
		// encoding/json skips both kinds of field.
		if !field.IsExported() || name == "-" {
			continue
		}
		omit := slices.ContainsFunc(strings.Split(opts, ","), func(o string) bool { return o == "omitempty" || o == "omitzero" })
		fields = append(fields, jsonField{index: field.Index[0], name: name, omitEmpty: omit})
	}
	return fields
}

// parseLine decodes one JSONL line into an account and extra, the keys of the
// line that are not in known, as a JSON object in line order. extra is nil
// when the line has no such key.
func parseLine(line []byte, known map[string]bool) (acct account.Account, extra []byte, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(line))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return acct, nil, false
	}
	var buf bytes.Buffer
	for dec.More() {
		start := dec.InputOffset()
		tok, err := dec.Token()
		if err != nil {
			return acct, nil, false
		}
		// The text since the last value is the comma, any space and the key as the line spells it.
		key := bytes.TrimLeft(line[start:dec.InputOffset()], ", \t\r\n")
		// Inside an object, a token with no error before a value is a key.
		name, _ := tok.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return acct, nil, false
		}
		if known[name] {
			continue
		}
		if buf.Len() == 0 {
			buf.WriteByte('{')
		} else {
			buf.WriteByte(',')
		}
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(value)
	}
	// The walk above stops at the last value, so Unmarshal checks the rest of the line.
	if err := json.Unmarshal(line, &acct); err != nil {
		return acct, nil, false
	}
	if buf.Len() > 0 {
		buf.WriteByte('}')
		extra = buf.Bytes()
	}
	return acct, extra, true
}

// sameJSON reports whether a and b hold the same JSON value. Numbers compare
// by their text.
func sameJSON(a, b []byte) bool {
	va, okA := decodeJSON(a)
	vb, okB := decodeJSON(b)
	return okA && okB && reflect.DeepEqual(va, vb)
}

// decodeJSON decodes b with each number kept as its text.
func decodeJSON(b []byte) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	err := dec.Decode(&v)
	return v, err == nil
}
