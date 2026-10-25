package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"jagex_account_creator/internal/api"
	"jagex_account_creator/internal/config"
	"jagex_account_creator/internal/store"
)

// ListPresets returns every preset in name order. No preset holds an IMAP password.
func (s *apiServer) ListPresets(ctx context.Context, _ api.ListPresetsRequestObject) (api.ListPresetsResponseObject, error) {
	presets, err := s.store.Presets(ctx)
	if err != nil {
		return nil, err
	}
	serve := apiConfig(s.config())
	out := api.PresetList{Presets: make([]api.Preset, len(presets))}
	for i := range presets {
		c, err := decodeConfig(presets[i].Config, "preset", presets[i].ID)
		if err != nil {
			return nil, err
		}
		out.Presets[i] = presetAnswer(&presets[i], c, serve)
	}
	return api.ListPresets200JSONResponse(out), nil
}

// SavePreset validates the posted config as a run start does and saves it
// under the trimmed name. A preset with the same name gets the new config and
// keeps its id.
func (s *apiServer) SavePreset(ctx context.Context, r api.SavePresetRequestObject) (api.SavePresetResponseObject, error) {
	name, cfg, refused := s.checkPreset(r.Body)
	if refused != nil {
		return refused, nil
	}
	p, err := s.store.SavePreset(ctx, name, configJSON(cfg), time.Now())
	if err != nil {
		return nil, err
	}
	s.logger.Info("preset saved", "id", p.ID, "name", name)
	return api.SavePreset200JSONResponse(presetAnswer(&p, apiConfig(cfg), apiConfig(s.config()))), nil
}

// checkPreset returns the trimmed name and the validated config of the save b.
// When the save must stop, refused is its answer: a 400 for an empty name or a
// value that does not parse, or a 422 for an invalid config.
func (s *apiServer) checkPreset(b *api.PresetSave) (name string, cfg *config.Config, refused api.SavePresetResponseObject) {
	name = strings.TrimSpace(b.Name)
	if name == "" {
		return "", nil, api.SavePreset400JSONResponse{Message: "name must not be empty"}
	}
	cfg, result, err := validatePosted(b.Config, s.config())
	switch {
	case err != nil:
		return "", nil, api.SavePreset400JSONResponse{Message: err.Error()}
	case !result.Valid:
		return "", nil, api.SavePreset422JSONResponse(result)
	}
	return name, cfg, nil
}

// DeletePreset deletes one preset.
func (s *apiServer) DeletePreset(ctx context.Context, r api.DeletePresetRequestObject) (api.DeletePresetResponseObject, error) {
	err := s.store.DeletePreset(ctx, r.Id)
	if errors.Is(err, store.ErrNotFound) {
		return api.DeletePreset404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	s.logger.Info("preset deleted", "id", r.Id)
	return api.DeletePreset204Response{}, nil
}

// presetAnswer returns the API form of p with the config c. The row keeps the
// stored form of runs.config, but database and imap_pass_set describe serve,
// not the preset, so they come from serve, the API form of the config of serve.
func presetAnswer(p *store.Preset, c, serve api.Config) api.Preset {
	c.Database, c.Email.ImapPassSet = serve.Database, serve.Email.ImapPassSet
	return api.Preset{Id: p.ID, Name: p.Name, SavedAt: p.SavedAt, Config: c}
}
