package store

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// exportFunc is one export of the store.
type exportFunc func(s *Store, ctx context.Context, w io.Writer, f AccountFilter) error

// exportText returns the export of s with f.
func exportText(t *testing.T, s *Store, export exportFunc, f AccountFilter) string {
	t.Helper()
	var buf bytes.Buffer
	if err := export(s, t.Context(), &buf, f); err != nil {
		t.Fatalf("export: %v", err)
	}
	return buf.String()
}

const csvHeader = "email,username,password,totp_secret,backup_codes,created_at,proxy," +
	"browser_bytes_sent,browser_bytes_received,http_bytes_sent,http_bytes_received,unconfirmed\n"

func TestExports(t *testing.T) {
	csvRow2 := `b@example.com,userb,"p""b",,,2025-01-16T00:00:00Z,2.2.2.2:80,0,0,0,0,true` + "\n"
	tests := []struct {
		name   string
		export exportFunc
		filter AccountFilter
		want   string
	}{
		{
			name: "CSV", export: (*Store).ExportCSV,
			want: csvHeader +
				`a@example.com,usera,"p,a",S1,11 22,2025-01-15T10:30:00.5Z,1.1.1.1:80,1,2,3,4,false` + "\n" +
				csvRow2 +
				`c%_d@example.com,c_d,pc,,,2025-01-17T00:00:00Z,,0,0,0,0,false` + "\n" +
				`e@example.com,MixedCase,@pe,S4,,2025-01-15T10:30:00Z,1.1.1.1:80,0,0,0,0,true` + "\n",
		},
		{name: "CSV with a filter", export: (*Store).ExportCSV, filter: AccountFilter{RunID: 2}, want: csvHeader + csvRow2},
		{name: "CSV with no match", export: (*Store).ExportCSV, filter: AccountFilter{Query: "zzz"}, want: csvHeader},
		{
			name: "user:pass", export: (*Store).ExportUserPass,
			want: "a@example.com:p,a\nb@example.com:p\"b\nc%_d@example.com:pc\ne@example.com:@pe\n",
		},
		{name: "user:pass with a filter", export: (*Store).ExportUserPass, filter: AccountFilter{HasTOTP: new(true)}, want: "a@example.com:p,a\ne@example.com:@pe\n"},
		{name: "user:pass with no match", export: (*Store).ExportUserPass, filter: AccountFilter{Query: "zzz"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mustOpen(t)
			writeFixture(t, s)
			if got := exportText(t, s, tt.export, tt.filter); got != tt.want {
				t.Errorf("export =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestExports_WriteError(t *testing.T) {
	tests := []struct {
		name   string
		export exportFunc
		want   string
	}{
		{name: "CSV", export: (*Store).ExportCSV, want: "writing the CSV export: "},
		{name: "user:pass", export: (*Store).ExportUserPass, want: "writing the user:pass export: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mustOpen(t)
			writeFixture(t, s)
			diskFull := errors.New("disk full")
			err := tt.export(s, t.Context(), failWriter{diskFull}, AccountFilter{})
			if !errors.Is(err, diskFull) || !strings.HasPrefix(err.Error(), tt.want) {
				t.Errorf("export error = %v, want the write failure with the prefix %q", err, tt.want)
			}
		})
	}
}

// cellKinds holds each kind of field that csvCell formats, and a field that the JSON record skips.
type cellKinds struct {
	Skipped string    `json:"-"`
	Text    string    `json:"text"`
	Codes   []string  `json:"codes,omitempty"`
	Time    time.Time `json:"time"`
	Zero    time.Time `json:"zero,omitzero"`
	Count   int64     `json:"count"`
	Bytes   int64     `json:"bytes,omitempty"`
	Flag    bool      `json:"flag"`
	Seen    bool      `json:"seen,omitempty"`
}

func TestWriteCSV_Cells(t *testing.T) {
	when := time.Date(2025, 1, 15, 10, 30, 0, 120000000, time.UTC)
	records := []cellKinds{
		{Skipped: "x", Text: "a\nb", Codes: []string{"1", "2", "3"}, Time: when, Zero: when, Count: -5, Bytes: 7, Flag: true, Seen: true},
		// A zero text, list or time is an empty cell when the JSON record omits it. A bool or an int64 keeps its value.
		{Codes: []string{}},
	}
	var buf bytes.Buffer
	if err := writeCSV(&buf, records); err != nil {
		t.Fatalf("writeCSV: %v", err)
	}
	want := "text,codes,time,zero,count,bytes,flag,seen\n" +
		"\"a\nb\",1 2 3,2025-01-15T10:30:00.12Z,2025-01-15T10:30:00.12Z,-5,7,true,true\n" +
		",,0001-01-01T00:00:00Z,,0,0,false,false\n"
	if got := buf.String(); got != want {
		t.Errorf("CSV =\n%s\nwant\n%s", got, want)
	}
}

func TestWriteCSV_NoFormForAKind(t *testing.T) {
	tests := []struct {
		name  string
		write func(w io.Writer) error
	}{
		{name: "a value", write: func(w io.Writer) error {
			return writeCSV(w, []struct {
				Ratio float64 `json:"ratio"`
			}{{Ratio: 0.5}})
		}},
		// An empty omitempty value still needs a form.
		{name: "an empty omitempty value", write: func(w io.Writer) error {
			return writeCSV(w, []struct {
				Ratio float64 `json:"ratio,omitempty"`
			}{{}})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			const want = "writing the CSV column ratio: no CSV form for type float64"
			if err := tt.write(&buf); err == nil || err.Error() != want {
				t.Errorf("writeCSV error = %v, want %s", err, want)
			}
			if buf.Len() != 0 {
				t.Errorf("writeCSV wrote %q, want nothing", buf.String())
			}
		})
	}
}
