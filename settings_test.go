package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ptr(s string) *string { return &s }

func TestSettingsUpdatePersistAndReset(t *testing.T) {
	file := Config{
		RTMPURL: "rtmp://live.twitch.tv/app", StreamKey: "file-key", InputTimeout: time.Second,
		SettingsFile: filepath.Join(t.TempDir(), "settings.json"),
	}
	log := slog.New(slog.DiscardHandler)
	s := NewSettings(file, log)

	// Invalid changes are rejected as a whole.
	if _, err := s.Update(map[string]*string{"switching.input_timeout": ptr("2s"), "switching.live_stable_time": ptr("soon")}); err == nil {
		t.Fatal("expected an error for an invalid duration")
	}
	if got := s.Get().InputTimeout; got != time.Second {
		t.Fatalf("partial update applied: input_timeout = %v", got)
	}
	for _, bad := range []map[string]*string{
		{"switching.input_timeout": ptr("50ms")},
		{"srt.latency": ptr("1s")},     // not editable
		{"twitch.stream_key": ptr("")}, // can't remove the key
		{"log.level": ptr("verbose")},  // not an option
		{"twitch.rtmp_url": ptr("http://x")},
	} {
		if _, err := s.Update(bad); err == nil {
			t.Errorf("expected an error for %v", bad)
		}
	}

	if _, err := s.Update(map[string]*string{"switching.input_timeout": ptr("2s"), "twitch.stream_key": ptr("ui-key")}); err != nil {
		t.Fatal(err)
	}
	if c := s.Get(); c.InputTimeout != 2*time.Second || c.StreamKey != "ui-key" {
		t.Fatalf("update not applied: %+v", c)
	}

	// The overrides survive a restart.
	s2 := NewSettings(file, log)
	s2.Load()
	if c := s2.Get(); c.InputTimeout != 2*time.Second || c.StreamKey != "ui-key" {
		t.Fatalf("overrides not loaded: %+v", c)
	}

	// Secrets never leave the server.
	for _, v := range s2.View().Settings {
		if v.Key == "twitch.stream_key" && (v.Value != "" || v.FileValue != "" || !v.IsSet || !v.Overridden) {
			t.Fatalf("stream key view leaks or is wrong: %+v", v)
		}
	}

	// null resets to the config file value.
	if _, err := s2.Update(map[string]*string{"switching.input_timeout": nil}); err != nil {
		t.Fatal(err)
	}
	if got := s2.Get().InputTimeout; got != time.Second {
		t.Fatalf("reset failed: input_timeout = %v", got)
	}
}

func TestSettingsMigrateLegacyKeysAndScenes(t *testing.T) {
	dir := t.TempDir()
	file := Config{
		RTMPURL: "rtmp://live.twitch.tv/app", InputTimeout: time.Second,
		Scenes:        []Scene{{ID: "brb", Name: "BRB", File: "brb.mp4"}, {ID: "end", Name: "Ending", File: "end.mp4"}},
		FallbackScene: "brb",
		SettingsFile:  filepath.Join(dir, "settings.json"),
	}
	// Written by the version that was configured with environment variables.
	if err := os.WriteFile(file.SettingsFile, []byte(`{"TWITCH_STREAM_KEY": "old-key", "INPUT_TIMEOUT": "2s"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewSettings(file, slog.New(slog.DiscardHandler))
	s.Load()
	if c := s.Get(); c.StreamKey != "old-key" || c.InputTimeout != 2*time.Second {
		t.Fatalf("legacy overrides not loaded: %+v", c)
	}
	b, _ := os.ReadFile(file.SettingsFile)
	if !strings.Contains(string(b), `"twitch.stream_key"`) || strings.Contains(string(b), "TWITCH_STREAM_KEY") {
		t.Fatalf("settings file not migrated: %s", b)
	}

	if _, err := s.Update(map[string]*string{"switching.fallback_scene": ptr("nope")}); err == nil {
		t.Fatal("expected an error for an unknown scene")
	}
	if _, err := s.Update(map[string]*string{"switching.fallback_scene": ptr("end")}); err != nil {
		t.Fatal(err)
	}
	if got := s.Get().FallbackScene; got != "end" {
		t.Fatalf("fallback scene = %q", got)
	}
	for _, v := range s.View().Settings {
		if v.Key == "switching.fallback_scene" && (len(v.Options) != 2 || v.Options[1].Label != "Ending") {
			t.Fatalf("scene options wrong: %+v", v.Options)
		}
	}
}
