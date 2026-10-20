package main

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

func ptr(s string) *string { return &s }

func TestSettingsUpdatePersistAndReset(t *testing.T) {
	env := Config{
		RTMPURL: "rtmp://live.twitch.tv/app", StreamKey: "env-key", InputTimeout: time.Second,
		SettingsFile: filepath.Join(t.TempDir(), "settings.json"),
	}
	log := slog.New(slog.DiscardHandler)
	s := NewSettings(env, log)

	// Invalid changes are rejected as a whole.
	if _, err := s.Update(map[string]*string{"INPUT_TIMEOUT": ptr("2s"), "LIVE_STABLE_TIME": ptr("soon")}); err == nil {
		t.Fatal("expected an error for an invalid duration")
	}
	if got := s.Get().InputTimeout; got != time.Second {
		t.Fatalf("partial update applied: INPUT_TIMEOUT = %v", got)
	}
	for _, bad := range []map[string]*string{
		{"INPUT_TIMEOUT": ptr("50ms")},
		{"SRT_LATENCY": ptr("1s")},     // not editable
		{"TWITCH_STREAM_KEY": ptr("")}, // can't remove the key
		{"LOG_LEVEL": ptr("verbose")},  // not an option
		{"TWITCH_RTMP_URL": ptr("http://x")},
	} {
		if _, err := s.Update(bad); err == nil {
			t.Errorf("expected an error for %v", bad)
		}
	}

	if _, err := s.Update(map[string]*string{"INPUT_TIMEOUT": ptr("2s"), "TWITCH_STREAM_KEY": ptr("ui-key")}); err != nil {
		t.Fatal(err)
	}
	if c := s.Get(); c.InputTimeout != 2*time.Second || c.StreamKey != "ui-key" {
		t.Fatalf("update not applied: %+v", c)
	}

	// The overrides survive a restart.
	s2 := NewSettings(env, log)
	s2.Load()
	if c := s2.Get(); c.InputTimeout != 2*time.Second || c.StreamKey != "ui-key" {
		t.Fatalf("overrides not loaded: %+v", c)
	}

	// Secrets never leave the server.
	for _, v := range s2.View().Settings {
		if v.Key == "TWITCH_STREAM_KEY" && (v.Value != "" || v.EnvValue != "" || !v.IsSet || !v.Overridden) {
			t.Fatalf("stream key view leaks or is wrong: %+v", v)
		}
	}

	// null resets to the environment.
	if _, err := s2.Update(map[string]*string{"INPUT_TIMEOUT": nil}); err != nil {
		t.Fatal(err)
	}
	if got := s2.Get().InputTimeout; got != time.Second {
		t.Fatalf("reset failed: INPUT_TIMEOUT = %v", got)
	}
}
