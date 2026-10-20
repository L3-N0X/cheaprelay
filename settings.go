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

// Settings holds the current configuration: the environment plus the changes
// made in the web UI, which are saved to SETTINGS_FILE and override the
// environment. Get is cheap and safe to call from any goroutine.
type Settings struct {
	log *slog.Logger
	env Config

	cur atomic.Pointer[Config]

	mu        sync.Mutex
	overrides map[string]string
	saveErr   error

	// OnChange is called after an update with the new configuration.
	OnChange func(Config)
}

func NewSettings(env Config, log *slog.Logger) *Settings {
	s := &Settings{log: log.With("component", "settings"), env: env, overrides: map[string]string{}}
	s.cur.Store(&env)
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
	path := s.env.SettingsFile
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
	for k, v := range saved {
		d, ok := settingDefByEnv(k)
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
// none. A nil value resets the setting to the environment value.
func (s *Settings) Update(changes map[string]*string) (Config, error) {
	s.mu.Lock()
	next := maps.Clone(s.overrides)
	for k, v := range changes {
		d, ok := settingDefByEnv(k)
		if !ok {
			s.mu.Unlock()
			return Config{}, fmt.Errorf("unknown setting %s", k)
		}
		if !d.Editable {
			s.mu.Unlock()
			return Config{}, fmt.Errorf("%s can only be changed in .env", k)
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
		err = errors.New("TWITCH_STREAM_KEY: a stream key is required")
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

// build returns the environment config with the overrides applied.
func (s *Settings) build(overrides map[string]string) (Config, error) {
	c := s.env
	for k, v := range overrides {
		d, _ := settingDefByEnv(k)
		if err := d.bind.set(&c, v); err != nil {
			return Config{}, fmt.Errorf("%s: %w", k, err)
		}
	}
	return c, c.validate()
}

func (s *Settings) save() error {
	path := s.env.SettingsFile
	if path == "" {
		return errors.New("SETTINGS_FILE is not set, changes are lost on restart")
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
	Key        string      `json:"key"`
	Label      string      `json:"label"`
	Group      string      `json:"group"`
	Kind       settingKind `json:"kind"`
	Options    []string    `json:"options,omitempty"`
	Help       string      `json:"help,omitempty"`
	Applies    string      `json:"applies"`
	Editable   bool        `json:"editable"`
	Value      string      `json:"value"`
	EnvValue   string      `json:"env_value"`
	IsSet      bool        `json:"is_set"`
	Overridden bool        `json:"overridden"`
}

type SettingsView struct {
	Persisted bool          `json:"persisted"`
	SaveError string        `json:"save_error,omitempty"`
	File      string        `json:"file,omitempty"`
	Settings  []SettingView `json:"settings"`
}

func (s *Settings) View() SettingsView {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.Get()
	v := SettingsView{File: s.env.SettingsFile, Persisted: s.env.SettingsFile != "" && s.saveErr == nil}
	if s.saveErr != nil {
		v.SaveError = s.saveErr.Error()
	}
	for _, d := range settingDefs {
		sv := SettingView{
			Key: d.Env, Label: d.Label, Group: d.Group, Kind: d.Kind, Options: d.Options,
			Help: d.Help, Applies: d.Applies, Editable: d.Editable,
			Value: d.bind.get(&cur), EnvValue: d.bind.get(&s.env),
		}
		_, sv.Overridden = s.overrides[d.Env]
		sv.IsSet = sv.Value != ""
		if d.Kind == kindSecret {
			sv.Value, sv.EnvValue = "", ""
		}
		v.Settings = append(v.Settings, sv)
	}
	return v
}
