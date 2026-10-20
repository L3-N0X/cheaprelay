package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfig(t *testing.T) {
	path := writeConfig(t, `
[twitch]
stream_key = "live_123"
autostart = true

[srt]
latency = "1s"

[switching]
input_timeout = "2s"
fallback_scene = "brb"

[[scenes]]
id = "starting"
name = "Starting soon"
file = "starting.mp4"

[[scenes]]
id = "brb"
file = "/media/brb.mp4"
`)
	c, _, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.StreamKey != "live_123" || !c.Autostart || c.SRTLatency != time.Second || c.InputTimeout != 2*time.Second {
		t.Fatalf("settings not applied: %+v", c)
	}
	if c.LiveStableTime != 3*time.Second || c.RTMPURL != "rtmp://live.twitch.tv/app" {
		t.Fatalf("defaults not applied: %+v", c)
	}
	if len(c.Scenes) != 2 || c.FallbackScene != "brb" {
		t.Fatalf("scenes: %+v, fallback %q", c.Scenes, c.FallbackScene)
	}
	if want := filepath.Join(filepath.Dir(path), "starting.mp4"); c.Scenes[0].File != want {
		t.Errorf("relative file not resolved: %q, want %q", c.Scenes[0].File, want)
	}
	if c.Scenes[1].Name != "brb" {
		t.Errorf("name should default to the id, got %q", c.Scenes[1].Name)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	c, warnings, err := LoadConfig(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) == 0 {
		t.Error("expected a warning about the missing file")
	}
	if len(c.Scenes) != 1 || c.FallbackScene != "fallback" || c.Scenes[0].File != "/media/fallback.mp4" {
		t.Fatalf("default scene wrong: %+v, fallback %q", c.Scenes, c.FallbackScene)
	}
}

func TestLoadConfigErrors(t *testing.T) {
	for name, tc := range map[string]struct{ toml, want string }{
		"unknown key":        {"[twitch]\nstreamkey = \"x\"", "twitch.streamkey: unknown setting"},
		"unknown section":    {"[foo]\nbar = 1", "foo.bar: unknown setting"},
		"top-level key":      {"stream_key = \"x\"", "inside a section"},
		"bad duration":       {"[switching]\ninput_timeout = 5", "invalid duration"},
		"unknown scene key":  {"[[scenes]]\nid = \"a\"\nfile = \"a.mp4\"\nloop = true", "scenes.loop: unknown setting"},
		"duplicate scene":    {"[[scenes]]\nid = \"a\"\nfile = \"a.mp4\"\n[[scenes]]\nid = \"a\"\nfile = \"b.mp4\"", "used twice"},
		"bad scene id":       {"[[scenes]]\nid = \"Be Right Back\"\nfile = \"a.mp4\"", "must be lowercase"},
		"scene without file": {"[[scenes]]\nid = \"a\"", "has no file"},
		"unknown fallback":   {"[switching]\nfallback_scene = \"nope\"", "no scene \"nope\""},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := LoadConfig(writeConfig(t, tc.toml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestLoadConfigWarnsAboutLegacyEnv(t *testing.T) {
	t.Setenv("TWITCH_STREAM_KEY", "x")
	_, warnings, _ := LoadConfig("")
	if len(warnings) != 1 || !strings.Contains(warnings[0], "twitch.stream_key") {
		t.Fatalf("warnings = %v", warnings)
	}
}
