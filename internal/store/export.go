package store

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"

	"jagex_account_creator/internal/account"
)

// ExportCSV writes the accounts that f selects to w as CSV, in id order. The
// header holds the JSON names of the account.Account fields in declaration
// order. The unknown keys of an imported line stay out.
// Limitation: a password can start with @, which a spreadsheet reads as a formula. The cell keeps the real value.
func (s *Store) ExportCSV(ctx context.Context, w io.Writer, f AccountFilter) error {
	stored, err := s.exportRows(ctx, f)
	if err != nil {
		return err
	}
	accounts := make([]account.Account, len(stored))
	for i := range stored {
		accounts[i] = stored[i].Account
	}
	return writeCSV(w, accounts)
}

// ExportUserPass writes one email:password line to w for each account that f
// selects, in id order. Jagex logs in with the email. The username is the display name.
func (s *Store) ExportUserPass(ctx context.Context, w io.Writer, f AccountFilter) error {
	stored, err := s.exportRows(ctx, f)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	for i := range stored {
		buf.WriteString(stored[i].Account.Email + ":" + stored[i].Account.Password + "\n")
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("writing the user:pass export: %w", err)
	}
	return nil
}

// writeCSV writes a header of the JSON names of the fields of T, then one row
// for each record. Each field of the JSON record is a column of every row.
func writeCSV[T any](w io.Writer, records []T) error {
	fields := jsonFields(reflect.TypeFor[T]())
	header := make([]string, len(fields))
	for i, f := range fields {
		header[i] = f.name
	}
	table := append(make([][]string, 0, len(records)+1), header)
	for i := range records {
		v := reflect.ValueOf(&records[i]).Elem()
		row := make([]string, len(fields))
		for j, f := range fields {
			cell, err := csvCell(v.Field(f.index), f.omitEmpty)
			if err != nil {
				return fmt.Errorf("writing the CSV column %s: %w", f.name, err)
			}
			row[j] = cell
		}
		table = append(table, row)
	}
	if err := csv.NewWriter(w).WriteAll(table); err != nil {
		return fmt.Errorf("writing the CSV export: %w", err)
	}
	return nil
}

// csvCell formats one field value. A bool or an int64 always prints its value.
// A zero text, list or time of an omitEmpty field is an empty cell. A type with
// no form here is an error, so a new field kind cannot reach the file in a form
// that no one chose.
func csvCell(v reflect.Value, omitEmpty bool) (string, error) {
	var text string
	switch x := v.Interface().(type) {
	case bool:
		return strconv.FormatBool(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case string:
		text = x
	case []string:
		text = strings.Join(x, " ")
	case time.Time:
		// The same form as the JSON record.
		text = x.Format(time.RFC3339Nano)
	default:
		return "", fmt.Errorf("no CSV form for type %s", v.Type())
	}
	// An empty non-nil []string is not zero, but its cell is empty too.
	if omitEmpty && v.IsZero() {
		return "", nil
	}
	return text, nil
}
