package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Preset is one named run config.
type Preset struct {
	ID      int64
	Name    string
	Config  json.RawMessage // a JSON object
	SavedAt time.Time
}

// savePresetSQL updates the row of a name that the store holds, so the preset keeps its id.
// It returns the columns in the order of presetRow.dest.
const savePresetSQL = `INSERT INTO presets (name, config, saved_at) VALUES (?, ?, ?)
ON CONFLICT (name) DO UPDATE SET config = excluded.config, saved_at = excluded.saved_at
RETURNING id, name, config, saved_at`

const deletePresetSQL = `DELETE FROM presets WHERE id = ?`

// selectPresetsSQL lists the columns in the order of presetRow.dest. The BINARY
// collation of a bare ORDER BY puts Zebra before apple, so the order uses NOCASE.
const selectPresetsSQL = `SELECT id, name, config, saved_at FROM presets ORDER BY name COLLATE NOCASE, id`

// Presets returns every preset in name order. The order ignores the case of ASCII letters.
func (s *Store) Presets(ctx context.Context) ([]Preset, error) {
	rows, err := queryRows(ctx, s.db, "presets", (*presetRow).dest, selectPresetsSQL)
	if err != nil {
		return nil, err
	}
	return convertRows(rows, (*presetRow).preset)
}

// SavePreset saves config, a JSON object, under name at savedAt and returns the
// preset. A preset that the store holds under name gets the new config and
// keeps its id.
// Limitation: the UNIQUE on name is binary, so Fast and fast are two presets.
func (s *Store) SavePreset(ctx context.Context, name string, config json.RawMessage, savedAt time.Time) (Preset, error) {
	var row presetRow
	if err := s.db.QueryRowContext(ctx, savePresetSQL, name, string(config), savedAt.UTC().Format(timeLayout)).Scan(row.dest()...); err != nil {
		return Preset{}, fmt.Errorf("saving preset %q: %w", name, err)
	}
	return row.preset()
}

// DeletePreset deletes the preset whose id is id. The error matches ErrNotFound
// when no preset has that id.
func (s *Store) DeletePreset(ctx context.Context, id int64) error {
	if err := execByID(ctx, s.db, deletePresetSQL, id); err != nil {
		return fmt.Errorf("deleting preset %d: %w", id, err)
	}
	return nil
}

// presetRow is one row of the presets table, with the config and the time as text.
type presetRow struct {
	p             Preset
	config, saved string
}

func (x *presetRow) dest() []any {
	return []any{&x.p.ID, &x.p.Name, &x.config, &x.saved}
}

// preset returns the preset of x with its time parsed.
func (x *presetRow) preset() (Preset, error) {
	p := x.p
	p.Config = json.RawMessage(x.config)
	var err error
	if p.SavedAt, err = time.Parse(timeLayout, x.saved); err != nil {
		return p, fmt.Errorf("reading saved_at of preset %d: %w", p.ID, err)
	}
	return p, nil
}
