package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is read from a TOML file (see config.example.toml). Settings marked
// editable in settingDefs can also be changed from the web UI (see
// settings.go); those changes override the file.
type Config struct {
	SRTListenAddr string
	SRTPassphrase string
	SRTStreamID   string
	SRTLatency    time.Duration

	RTMPURL   string
	StreamKey string

	Scenes              []Scene
	FallbackScene       string
	InputTimeout        time.Duration
	LiveStableTime      time.Duration
	FallbackMaxDuration time.Duration

	APIListenAddr string
	APIToken      string
	Autostart     bool
	Preview       bool
	SettingsFile  string

	FFmpegPath      string
	FFmpegLogLevel  string
	AnalyzeDuration time.Duration
	LogLevel        string

	// ConfigFile is the file this was loaded from, empty if it didn't exist.
	ConfigFile string
}

// Scene is a video that can be streamed instead of the phone: shown
// automatically when the phone drops (the fallback scene) or picked by hand,
// e.g. a "starting soon", "be right back" or "ending" screen.
type Scene struct {
	ID   string `toml:"id" json:"id"`
	Name string `toml:"name" json:"name"`
	File string `toml:"file" json:"-"`
}

// defaultScenes is used when the config file defines none.
var defaultScenes = []Scene{{ID: "fallback", Name: "Fallback", File: "/media/fallback.mp4"}}

var sceneIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func (c Config) Scene(id string) (Scene, bool) {
	for _, s := range c.Scenes {
		if s.ID == id {
			return s, true
		}
	}
	return Scene{}, false
}

type settingKind string

const (
	kindText     settingKind = "text"
	kindSecret   settingKind = "secret"
	kindDuration settingKind = "duration"
	kindBool     settingKind = "bool"
	kindSelect   settingKind = "select"
	kindScene    settingKind = "scene" // a select with the configured scenes as options
)

// When a changed setting takes effect, shown in the web UI.
const (
	appliesNow         = "immediately"
	appliesNextTwitch  = "next Twitch connection"
	appliesNextPhone   = "next phone connection"
	appliesNextStart   = "next relay start"
	appliesFileOnly    = "set in the config file, needs a restart"
	appliesNextProcess = "next ffmpeg start"
)

type settingDef struct {
	Key      string // "section.name" in the config file
	Label    string
	Group    string
	Kind     settingKind
	Options  []string
	Default  string
	Help     string
	Applies  string
	Editable bool
	bind     binding

	// legacyEnv is the environment variable this setting used to be read
	// from. Only used to migrate old settings files and to warn about
	// leftover variables.
	legacyEnv string
}

var settingDefs = []settingDef{
	{Key: "twitch.stream_key", legacyEnv: "TWITCH_STREAM_KEY", Label: "Stream key", Group: "Twitch", Kind: kindSecret, Editable: true, Applies: appliesNextTwitch,
		Help: "Twitch dashboard → Settings → Stream. Append ?bandwidthtest=true to test without going public.",
		bind: strBind(func(c *Config) *string { return &c.StreamKey })},
	{Key: "twitch.rtmp_url", legacyEnv: "TWITCH_RTMP_URL", Label: "Ingest server", Group: "Twitch", Kind: kindText, Editable: true, Applies: appliesNextTwitch,
		Default: "rtmp://live.twitch.tv/app", Help: "RTMP URL without the key. The default picks the closest server automatically.",
		bind: strBind(func(c *Config) *string { return &c.RTMPURL })},
	{Key: "twitch.autostart", legacyEnv: "AUTOSTART", Label: "Autostart", Group: "Twitch", Kind: kindBool, Editable: true, Applies: appliesNextStart,
		Default: "false", Help: "Go live on Twitch as soon as the relay starts.",
		bind: boolBind(func(c *Config) *bool { return &c.Autostart })},

	{Key: "srt.passphrase", legacyEnv: "SRT_PASSPHRASE", Label: "Passphrase", Group: "Phone (SRT)", Kind: kindSecret, Editable: true, Applies: appliesNextPhone,
		Help: "10–79 characters, the phone app needs the same one. Empty disables encryption.",
		bind: strBind(func(c *Config) *string { return &c.SRTPassphrase })},
	{Key: "srt.stream_id", legacyEnv: "SRT_STREAM_ID", Label: "Stream ID", Group: "Phone (SRT)", Kind: kindText, Editable: true, Applies: appliesNextPhone,
		Help: "Only accept connections with exactly this stream id. Empty accepts any.",
		bind: strBind(func(c *Config) *string { return &c.SRTStreamID })},
	{Key: "srt.latency", legacyEnv: "SRT_LATENCY", Label: "Latency", Group: "Phone (SRT)", Kind: kindDuration, Applies: appliesFileOnly,
		Default: "2s", Help: "Receive buffer. The higher of this and the phone's setting is used.",
		bind: durBind(func(c *Config) *time.Duration { return &c.SRTLatency })},
	{Key: "srt.listen_addr", legacyEnv: "SRT_LISTEN_ADDR", Label: "Listen address", Group: "Phone (SRT)", Kind: kindText, Applies: appliesFileOnly,
		Default: ":9000", bind: strBind(func(c *Config) *string { return &c.SRTListenAddr })},

	{Key: "switching.fallback_scene", Label: "Fallback scene", Group: "Switching", Kind: kindScene, Editable: true, Applies: appliesNow,
		Help: "Shown automatically when the phone drops. Empty = the first scene in the config file.",
		bind: strBind(func(c *Config) *string { return &c.FallbackScene })},
	{Key: "switching.input_timeout", legacyEnv: "INPUT_TIMEOUT", Label: "Input timeout", Group: "Switching", Kind: kindDuration, Editable: true, Applies: appliesNow,
		Default: "1500ms", Help: "No data from the phone for this long switches to the fallback scene.",
		bind: durBind(func(c *Config) *time.Duration { return &c.InputTimeout })},
	{Key: "switching.live_stable_time", legacyEnv: "LIVE_STABLE_TIME", Label: "Live stable time", Group: "Switching", Kind: kindDuration, Editable: true, Applies: appliesNow,
		Default: "3s", Help: "Data must flow this long before switching from the fallback back to live.",
		bind: durBind(func(c *Config) *time.Duration { return &c.LiveStableTime })},
	{Key: "switching.fallback_max_duration", legacyEnv: "FALLBACK_MAX_DURATION", Label: "Max fallback time", Group: "Switching", Kind: kindDuration, Editable: true, Applies: appliesNow,
		Default: "0", Help: "End the stream when the phone has been gone this long. 0 = never. Doesn't apply to a scene picked by hand.",
		bind: durBind(func(c *Config) *time.Duration { return &c.FallbackMaxDuration })},

	{Key: "web.preview", legacyEnv: "PREVIEW_ENABLED", Label: "Input preview", Group: "Web UI", Kind: kindBool, Editable: true, Applies: appliesNow,
		Default: "true", Help: "Allow the web UI to show snapshots of the phone's stream (decodes one keyframe per second, only while someone watches).",
		bind: boolBind(func(c *Config) *bool { return &c.Preview })},
	{Key: "web.api_token", legacyEnv: "API_TOKEN", Label: "API token", Group: "Web UI", Kind: kindSecret, Applies: appliesFileOnly,
		bind: strBind(func(c *Config) *string { return &c.APIToken })},
	{Key: "web.listen_addr", legacyEnv: "API_LISTEN_ADDR", Label: "Listen address", Group: "Web UI", Kind: kindText, Applies: appliesFileOnly,
		Default: ":8080", bind: strBind(func(c *Config) *string { return &c.APIListenAddr })},
	{Key: "web.settings_file", legacyEnv: "SETTINGS_FILE", Label: "Settings file", Group: "Web UI", Kind: kindText, Applies: appliesFileOnly,
		Default: "/data/settings.json", Help: "Where changes made here are saved. They override the config file.",
		bind: strBind(func(c *Config) *string { return &c.SettingsFile })},

	{Key: "ffmpeg.analyze_duration", legacyEnv: "FFMPEG_ANALYZE_DURATION", Label: "Analyze duration", Group: "Advanced", Kind: kindDuration, Editable: true, Applies: appliesNextProcess,
		Default: "1s", Help: "How much input ffmpeg analyses before connecting to Twitch.",
		bind: durBind(func(c *Config) *time.Duration { return &c.AnalyzeDuration })},
	{Key: "ffmpeg.log_level", legacyEnv: "FFMPEG_LOGLEVEL", Label: "ffmpeg log level", Group: "Advanced", Kind: kindSelect, Editable: true, Applies: appliesNextProcess,
		Default: "warning", Options: []string{"quiet", "error", "warning", "info", "verbose", "debug"},
		bind: strBind(func(c *Config) *string { return &c.FFmpegLogLevel })},
	{Key: "log.level", legacyEnv: "LOG_LEVEL", Label: "Relay log level", Group: "Advanced", Kind: kindSelect, Editable: true, Applies: appliesNow,
		Default: "info", Options: []string{"debug", "info", "warn", "error"},
		bind: strBind(func(c *Config) *string { return &c.LogLevel })},
	{Key: "ffmpeg.path", legacyEnv: "FFMPEG_PATH", Label: "ffmpeg binary", Group: "Advanced", Kind: kindText, Applies: appliesFileOnly,
		Default: "ffmpeg", bind: strBind(func(c *Config) *string { return &c.FFmpegPath })},
}

func settingDefByKey(key string) (settingDef, bool) {
	for _, d := range settingDefs {
		if d.Key == key {
			return d, true
		}
	}
	return settingDef{}, false
}

func settingDefByLegacyEnv(env string) (settingDef, bool) {
	for _, d := range settingDefs {
		if d.legacyEnv != "" && d.legacyEnv == env {
			return d, true
		}
	}
	return settingDef{}, false
}

// LoadConfig reads the config file. A missing file is not an error, the
// defaults are used then (the stream key can be set from the web UI). The
// returned warnings are for things that are worth logging but don't prevent
// the relay from starting.
func LoadConfig(path string) (c Config, warnings []string, err error) {
	var errs []error
	for _, d := range settingDefs {
		_ = d.bind.set(&c, d.Default)
	}
	for _, d := range settingDefs {
		if _, ok := os.LookupEnv(d.legacyEnv); d.legacyEnv != "" && ok {
			warnings = append(warnings, fmt.Sprintf("environment variable %s is no longer read, set %s in the config file instead", d.legacyEnv, d.Key))
		}
	}

	if path != "" {
		switch _, statErr := os.Stat(path); {
		case statErr == nil:
			c.ConfigFile = path
			if err := c.readFile(path); err != nil {
				errs = append(errs, err)
			}
		case errors.Is(statErr, os.ErrNotExist):
			warnings = append(warnings, fmt.Sprintf("config file %s not found, using the defaults", path))
		default:
			errs = append(errs, fmt.Errorf("config file: %w", statErr))
		}
	}
	if len(c.Scenes) == 0 {
		c.Scenes = slices.Clone(defaultScenes)
	}
	if err := c.validate(); err != nil {
		errs = append(errs, err)
	}
	return c, warnings, errors.Join(errs...)
}

// readFile applies the settings and scenes from a TOML file. Unknown keys are
// errors, so a typo never silently falls back to a default.
func (c *Config) readFile(path string) error {
	var raw map[string]toml.Primitive
	md, err := toml.DecodeFile(path, &raw)
	if err != nil {
		return fmt.Errorf("config file %s: %w", path, err)
	}

	var errs []error
	for section, prim := range raw {
		if section == "scenes" {
			if err := md.PrimitiveDecode(prim, &c.Scenes); err != nil {
				errs = append(errs, fmt.Errorf("scenes: %w", err))
			}
			continue
		}
		var v any
		_ = md.PrimitiveDecode(prim, &v) // can't fail for any
		values, ok := v.(map[string]any)
		if !ok {
			errs = append(errs, fmt.Errorf("%s: settings must be inside a section like [twitch]", section))
			continue
		}
		for name, v := range values {
			key := section + "." + name
			d, ok := settingDefByKey(key)
			if !ok {
				errs = append(errs, fmt.Errorf("%s: unknown setting", key))
				continue
			}
			var s string
			switch v := v.(type) {
			case string:
				s = v
			case bool, int64, float64:
				s = fmt.Sprint(v)
			default:
				errs = append(errs, fmt.Errorf("%s: unsupported value %v", key, v))
				continue
			}
			if err := d.bind.set(c, s); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", key, err))
			}
		}
	}
	// Keys in the sections are checked above, this catches unknown scene keys.
	for _, k := range md.Undecoded() {
		if len(k) > 1 && k[0] == "scenes" {
			errs = append(errs, fmt.Errorf("%s: unknown setting", k))
		}
	}

	// Relative scene files are relative to the config file.
	for i := range c.Scenes {
		if f := c.Scenes[i].File; f != "" && !filepath.IsAbs(f) {
			c.Scenes[i].File = filepath.Join(filepath.Dir(path), f)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("config file %s: %w", path, err)
	}
	return nil
}

func (c *Config) validate() error {
	var errs []error
	c.RTMPURL = strings.TrimRight(c.RTMPURL, "/")
	if !strings.HasPrefix(c.RTMPURL, "rtmp://") && !strings.HasPrefix(c.RTMPURL, "rtmps://") {
		errs = append(errs, errors.New("twitch.rtmp_url: must start with rtmp:// or rtmps://"))
	}
	if n := len(c.SRTPassphrase); n != 0 && (n < 10 || n > 79) {
		errs = append(errs, errors.New("srt.passphrase: must be between 10 and 79 characters (SRT limitation)"))
	}
	if c.InputTimeout < 200*time.Millisecond {
		errs = append(errs, errors.New("switching.input_timeout: must be at least 200ms"))
	}

	seen := map[string]bool{}
	for i := range c.Scenes {
		s := &c.Scenes[i]
		s.ID, s.Name, s.File = strings.TrimSpace(s.ID), strings.TrimSpace(s.Name), strings.TrimSpace(s.File)
		switch {
		case !sceneIDPattern.MatchString(s.ID):
			errs = append(errs, fmt.Errorf("scenes: id %q must be lowercase letters, digits, - and _", s.ID))
		case seen[s.ID]:
			errs = append(errs, fmt.Errorf("scenes: id %q is used twice", s.ID))
		case s.File == "":
			errs = append(errs, fmt.Errorf("scenes: %q has no file", s.ID))
		}
		seen[s.ID] = true
		if s.Name == "" {
			s.Name = s.ID
		}
	}
	if c.FallbackScene == "" && len(c.Scenes) > 0 {
		c.FallbackScene = c.Scenes[0].ID
	}
	if _, ok := c.Scene(c.FallbackScene); !ok && len(c.Scenes) > 0 {
		errs = append(errs, fmt.Errorf("switching.fallback_scene: there is no scene %q", c.FallbackScene))
	}
	return errors.Join(errs...)
}

// OutputURL is the full RTMP publish URL including the stream key.
func (c Config) OutputURL() string {
	return c.RTMPURL + "/" + c.StreamKey
}

// binding parses a setting from and formats it to a string, which is also
// what the web UI and the settings file use.
type binding interface {
	set(c *Config, v string) error
	get(c *Config) string
}

type strBind func(*Config) *string

func (b strBind) set(c *Config, v string) error { *b(c) = strings.TrimSpace(v); return nil }
func (b strBind) get(c *Config) string          { return *b(c) }

type durBind func(*Config) *time.Duration

func (b durBind) set(c *Config, v string) error {
	v = strings.TrimSpace(v)
	if v == "" || v == "0" {
		*b(c) = 0
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return fmt.Errorf("invalid duration %q (use e.g. \"1500ms\", \"3s\", \"10m\")", v)
	}
	*b(c) = d
	return nil
}

func (b durBind) get(c *Config) string {
	if d := *b(c); d != 0 {
		return d.String()
	}
	return "0"
}

type boolBind func(*Config) *bool

func (b boolBind) set(c *Config, v string) error {
	if strings.TrimSpace(v) == "" {
		*b(c) = false
		return nil
	}
	p, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return fmt.Errorf("invalid boolean %q", v)
	}
	*b(c) = p
	return nil
}

func (b boolBind) get(c *Config) string { return strconv.FormatBool(*b(c)) }
