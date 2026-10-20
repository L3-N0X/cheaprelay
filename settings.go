package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
)

// Settings holds the current configuration: the config file plus the changes
// made in the web UI, which are saved to web.settings_file and override the
// config file. Get is cheap and safe to call from any goroutine.
type Settings struct {
	log  *slog.Logger
	file Config

	cur atomic.Pointer[Config]

	mu        sync.Mutex
	overrides map[string]string
	saveErr   error

	// OnChange is called after an update with the new configuration.
	OnChange func(Config)
}

func NewSettings(file Config, log *slog.Logger) *Settings {
	s := &Settings{log: log.With("component", "settings"), file: file, overrides: map[string]string{}}
	s.cur.Store(&file)
	return s
}

// newStaticSettings is for tests: no settings file, no overrides.
func newStaticSettings(c Config) *Settings {
	return NewSettings(c, slog.New(slog.DiscardHandler))
}

func (s *Settings) Get() Config { return *s.cur.Load() }

// Load reads the settings file. Invalid or unknown entries are ignored with a
// warning so a bad file never prevents the relay from starting.
func (s *Settings) Load() {
	path := s.file.SettingsFile
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		s.log.Warn("could not read settings file", "path", path, "err", err)
		return
	}
	var saved map[string]string
	if err := json.Unmarshal(b, &saved); err != nil {
		s.log.Warn("ignoring invalid settings file", "path", path, "err", err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	migrated := false
	for k, v := range saved {
		d, ok := settingDefByKey(k)
		if !ok {
			// Written by a version that was configured with environment variables.
			if d, ok = settingDefByLegacyEnv(k); ok {
				k, migrated = d.Key, true
			}
		}
		if !ok || !d.Editable {
			s.log.Warn("ignoring unknown setting in settings file", "key", k)
			continue
		}
		next := maps.Clone(s.overrides)
		next[k] = v
		if _, err := s.build(next); err != nil {
			s.log.Warn("ignoring invalid setting in settings file", "key", k, "err", err)
			continue
		}
		s.overrides = next
	}
	c, _ := s.build(s.overrides)
	s.cur.Store(&c)
	if migrated {
		if err := s.save(); err != nil {
			s.log.Warn("could not save the migrated settings file", "path", path, "err", err)
		}
	}
	if len(s.overrides) > 0 {
		keys := make([]string, 0, len(s.overrides))
		for k := range s.overrides {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		s.log.Info("loaded settings from the web UI", "path", path, "overrides", keys)
	}
}

// Update applies changes atomically: either all are valid and applied, or
// none. A nil value resets the setting to the config file value.
func (s *Settings) Update(changes map[string]*string) (Config, error) {
	s.mu.Lock()
	next := maps.Clone(s.overrides)
	for k, v := range changes {
		d, ok := settingDefByKey(k)
		if !ok {
			s.mu.Unlock()
			return Config{}, fmt.Errorf("unknown setting %s", k)
		}
		if !d.Editable {
			s.mu.Unlock()
			return Config{}, fmt.Errorf("%s can only be changed in the config file", k)
		}
		if v == nil {
			delete(next, k)
			continue
		}
		if d.Kind == kindSelect && !slices.Contains(d.Options, *v) {
			s.mu.Unlock()
			return Config{}, fmt.Errorf("%s: must be one of %v", k, d.Options)
		}
		next[k] = *v
	}
	c, err := s.build(next)
	if err == nil && c.StreamKey == "" && s.Get().StreamKey != "" {
		err = errors.New("twitch.stream_key: a stream key is required")
	}
	if err != nil {
		s.mu.Unlock()
		return Config{}, err
	}
	s.overrides = next
	s.cur.Store(&c)
	s.saveErr = s.save()
	if s.saveErr != nil {
		s.log.Warn("could not save settings, changes are lost on restart", "err", s.saveErr)
	}
	s.mu.Unlock()

	keys := make([]string, 0, len(changes))
	for k := range changes {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	s.log.Info("settings changed", "keys", keys)
	if s.OnChange != nil {
		s.OnChange(c)
	}
	return c, nil
}

// build returns the config file values with the overrides applied.
func (s *Settings) build(overrides map[string]string) (Config, error) {
	c := s.file
	for k, v := range overrides {
		d, _ := settingDefByKey(k)
		if err := d.bind.set(&c, v); err != nil {
			return Config{}, fmt.Errorf("%s: %w", k, err)
		}
	}
	return c, c.validate()
}

func (s *Settings) save() error {
	path := s.file.SettingsFile
	if path == "" {
		return errors.New("web.settings_file is not set, changes are lost on restart")
	}
	b, err := json.MarshalIndent(s.overrides, "", "  ")
	if err != nil {
		return err
	}
	// Write and rename, so a crash never leaves a truncated file behind.
	// CreateTemp uses mode 0600, which matters because the file holds secrets.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// SettingView is one setting as shown in the web UI. Secrets are never sent
// to the browser, only whether they are set.
type SettingView struct {
	Key        string          `json:"key"`
	Label      string          `json:"label"`
	Group      string          `json:"group"`
	Kind       settingKind     `json:"kind"`
	Options    []settingOption `json:"options,omitempty"`
	Help       string          `json:"help,omitempty"`
	Applies    string          `json:"applies"`
	Editable   bool            `json:"editable"`
	Value      string          `json:"value"`
	FileValue  string          `json:"file_value"`
	IsSet      bool            `json:"is_set"`
	Overridden bool            `json:"overridden"`
}

type settingOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type SettingsView struct {
	Persisted  bool          `json:"persisted"`
	SaveError  string        `json:"save_error,omitempty"`
	File       string        `json:"file,omitempty"`        // where UI changes are saved
	ConfigFile string        `json:"config_file,omitempty"` // empty if there is none
	Settings   []SettingView `json:"settings"`
}

func (s *Settings) View() SettingsView {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.Get()
	v := SettingsView{
		File: s.file.SettingsFile, ConfigFile: s.file.ConfigFile,
		Persisted: s.file.SettingsFile != "" && s.saveErr == nil,
	}
	if s.saveErr != nil {
		v.SaveError = s.saveErr.Error()
	}
	for _, d := range settingDefs {
		sv := SettingView{
			Key: d.Key, Label: d.Label, Group: d.Group, Kind: d.Kind,
			Help: d.Help, Applies: d.Applies, Editable: d.Editable,
			Value: d.bind.get(&cur), FileValue: d.bind.get(&s.file),
		}
		for _, o := range d.Options {
			sv.Options = append(sv.Options, settingOption{Value: o, Label: o})
		}
		if d.Kind == kindScene {
			for _, sc := range cur.Scenes {
				sv.Options = append(sv.Options, settingOption{Value: sc.ID, Label: sc.Name})
			}
		}
		_, sv.Overridden = s.overrides[d.Key]
		sv.IsSet = sv.Value != ""
		if d.Kind == kindSecret {
			sv.Value, sv.FileValue = "", ""
		}
		v.Settings = append(v.Settings, sv)
	}
	return v
}
