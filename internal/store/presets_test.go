package store

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// presetsT0 returns the time of the first preset save.
func presetsT0() time.Time { return time.Date(2026, 3, 4, 5, 6, 7, 8, time.UTC) }

// mustSavePreset saves config under name at savedAt and returns the preset.
func mustSavePreset(t *testing.T, s *Store, name, config string, savedAt time.Time) Preset {
	t.Helper()
	p, err := s.SavePreset(t.Context(), name, json.RawMessage(config), savedAt)
	if err != nil {
		t.Fatalf("SavePreset %q: %v", name, err)
	}
	return p
}

// mustPresets returns the presets of s.
func mustPresets(t *testing.T, s *Store) []Preset {
	t.Helper()
	presets, err := s.Presets(t.Context())
	if err != nil {
		t.Fatalf("Presets: %v", err)
	}
	return presets
}

// samePreset tells whether a and b hold the same values. The config compares as text.
func samePreset(a, b Preset) bool {
	return a.ID == b.ID && a.Name == b.Name && string(a.Config) == string(b.Config) && a.SavedAt.Equal(b.SavedAt)
}

func TestPresets_Empty(t *testing.T) {
	presets := mustPresets(t, mustOpen(t))
	if presets == nil || len(presets) != 0 {
		t.Errorf("Presets = %#v, want an empty list", presets)
	}
}

func TestSavePreset_UpsertKeepsID(t *testing.T) {
	s := mustOpen(t)
	first := mustSavePreset(t, s, "fast", `{"debug":false}`, presetsT0())
	want := Preset{ID: 1, Name: "fast", Config: json.RawMessage(`{"debug":false}`), SavedAt: presetsT0()}
	if !samePreset(first, want) {
		t.Errorf("first save = %+v, want %+v", first, want)
	}
	other := mustSavePreset(t, s, "slow", `{}`, presetsT0())

	later := presetsT0().Add(time.Hour).In(time.FixedZone("UTC+2", 2*60*60))
	second := mustSavePreset(t, s, "fast", `{"debug":true}`, later)
	want = Preset{ID: 1, Name: "fast", Config: json.RawMessage(`{"debug":true}`), SavedAt: later}
	if !samePreset(second, want) || second.SavedAt.Location() != time.UTC {
		t.Errorf("second save = %+v, want %+v in UTC", second, want)
	}
	got := mustPresets(t, s)
	if len(got) != 2 || !samePreset(got[0], want) || !samePreset(got[1], other) {
		t.Errorf("Presets = %+v, want the new fast and slow", got)
	}
}

func TestPresets_NameOrderIgnoresCase(t *testing.T) {
	s := mustOpen(t)
	for _, name := range []string{"Zebra", "apple", "fast", "Fast", "Mango"} {
		mustSavePreset(t, s, name, `{}`, presetsT0())
	}
	presets := mustPresets(t, s)
	got := make([]string, len(presets))
	for i, p := range presets {
		got[i] = p.Name
	}
	// NOCASE ties fast and Fast, so the id order breaks the tie.
	if want := []string{"apple", "fast", "Fast", "Mango", "Zebra"}; !slices.Equal(got, want) {
		t.Errorf("names = %q, want %q", got, want)
	}
}

func TestDeletePreset(t *testing.T) {
	s := mustOpen(t)
	mustSavePreset(t, s, "a", `{}`, presetsT0())
	b := mustSavePreset(t, s, "b", `{}`, presetsT0())
	if err := s.DeletePreset(t.Context(), 1); err != nil {
		t.Fatalf("DeletePreset: %v", err)
	}
	if got := mustPresets(t, s); len(got) != 1 || !samePreset(got[0], b) {
		t.Errorf("Presets after the delete = %+v, want only b", got)
	}
	err := s.DeletePreset(t.Context(), 1)
	if !errors.Is(err, ErrNotFound) || err.Error() != "deleting preset 1: not found" {
		t.Errorf("second DeletePreset error = %v, want deleting preset 1: not found", err)
	}
}

func TestSavePreset_ChecksRefuse(t *testing.T) {
	tests := []struct{ name, preset, config, want string }{
		{name: "an empty name", preset: "", config: `{}`, want: `saving preset "": constraint failed: CHECK constraint failed: name <> ''`},
		{name: "a JSON array", preset: "a", config: `[]`, want: `saving preset "a": constraint failed: CHECK constraint failed: json_type(config) = 'object'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mustOpen(t)
			_, err := s.SavePreset(t.Context(), tt.preset, json.RawMessage(tt.config), presetsT0())
			if err == nil || !strings.HasPrefix(err.Error(), tt.want) {
				t.Errorf("SavePreset error = %v, want the prefix %q", err, tt.want)
			}
			if got := mustPresets(t, s); len(got) != 0 {
				t.Errorf("Presets = %+v, want none", got)
			}
		})
	}
}

func TestPresets_BadSavedAt(t *testing.T) {
	s := mustOpen(t)
	mustSavePreset(t, s, "a", `{}`, presetsT0())
	if _, err := s.db.ExecContext(t.Context(), `UPDATE presets SET saved_at = 'yesterday'`); err != nil {
		t.Fatal(err)
	}
	_, err := s.Presets(t.Context())
	if want := "reading saved_at of preset 1: "; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("Presets error = %v, want the prefix %q", err, want)
	}
}

func TestPresets_ClosedStore(t *testing.T) {
	s := mustOpen(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ctx := t.Context()
	calls := []struct {
		name string
		call func() error
		want string
	}{
		{name: "Presets", call: func() error { _, err := s.Presets(ctx); return err }, want: "reading presets: "},
		{name: "SavePreset", call: func() error { _, err := s.SavePreset(ctx, "a", json.RawMessage(`{}`), presetsT0()); return err }, want: `saving preset "a": `},
		{name: "DeletePreset", call: func() error { return s.DeletePreset(ctx, 1) }, want: "deleting preset 1: "},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			if err := c.call(); err == nil || err.Error() != c.want+"sql: database is closed" {
				t.Errorf("%s error = %v, want %ssql: database is closed", c.name, err, c.want)
			}
		})
	}
}
