package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is read from environment variables. See README.md for descriptions.
// Settings marked editable in settingDefs can also be changed from the web UI
// (see settings.go); those changes override the environment.
type Config struct {
	SRTListenAddr string
	SRTPassphrase string
	SRTStreamID   string
	SRTLatency    time.Duration

	RTMPURL   string
	StreamKey string

	FallbackFile        string
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
}

type settingKind string

const (
	kindText     settingKind = "text"
	kindSecret   settingKind = "secret"
	kindDuration settingKind = "duration"
	kindBool     settingKind = "bool"
	kindSelect   settingKind = "select"
)

// When a changed setting takes effect, shown in the web UI.
const (
	appliesNow         = "immediately"
	appliesNextTwitch  = "next Twitch connection"
	appliesNextPhone   = "next phone connection"
	appliesNextStart   = "next relay start"
	appliesEnvOnly     = "set in .env, needs a restart"
	appliesNextProcess = "next ffmpeg start"
)

type settingDef struct {
	Env      string
	Label    string
	Group    string
	Kind     settingKind
	Options  []string
	Default  string
	Help     string
	Applies  string
	Editable bool
	bind     binding
}

var settingDefs = []settingDef{
	{Env: "TWITCH_STREAM_KEY", Label: "Stream key", Group: "Twitch", Kind: kindSecret, Editable: true, Applies: appliesNextTwitch,
		Help: "Twitch dashboard → Settings → Stream. Append ?bandwidthtest=true to test without going public.",
		bind: strBind(func(c *Config) *string { return &c.StreamKey })},
	{Env: "TWITCH_RTMP_URL", Label: "Ingest server", Group: "Twitch", Kind: kindText, Editable: true, Applies: appliesNextTwitch,
		Default: "rtmp://live.twitch.tv/app", Help: "RTMP URL without the key. The default picks the closest server automatically.",
		bind: strBind(func(c *Config) *string { return &c.RTMPURL })},
	{Env: "AUTOSTART", Label: "Autostart", Group: "Twitch", Kind: kindBool, Editable: true, Applies: appliesNextStart,
		Default: "false", Help: "Go live on Twitch as soon as the relay starts.",
		bind: boolBind(func(c *Config) *bool { return &c.Autostart })},

	{Env: "SRT_PASSPHRASE", Label: "Passphrase", Group: "Phone (SRT)", Kind: kindSecret, Editable: true, Applies: appliesNextPhone,
		Help: "10–79 characters, the phone app needs the same one. Empty disables encryption.",
		bind: strBind(func(c *Config) *string { return &c.SRTPassphrase })},
	{Env: "SRT_STREAM_ID", Label: "Stream ID", Group: "Phone (SRT)", Kind: kindText, Editable: true, Applies: appliesNextPhone,
		Help: "Only accept connections with exactly this stream id. Empty accepts any.",
		bind: strBind(func(c *Config) *string { return &c.SRTStreamID })},
	{Env: "SRT_LATENCY", Label: "Latency", Group: "Phone (SRT)", Kind: kindDuration, Applies: appliesEnvOnly,
		Default: "2s", Help: "Receive buffer. The higher of this and the phone's setting is used.",
		bind: durBind(func(c *Config) *time.Duration { return &c.SRTLatency })},
	{Env: "SRT_LISTEN_ADDR", Label: "Listen address", Group: "Phone (SRT)", Kind: kindText, Applies: appliesEnvOnly,
		Default: ":9000", bind: strBind(func(c *Config) *string { return &c.SRTListenAddr })},

	{Env: "INPUT_TIMEOUT", Label: "Input timeout", Group: "Switching", Kind: kindDuration, Editable: true, Applies: appliesNow,
		Default: "1500ms", Help: "No data from the phone for this long switches to the fallback.",
		bind: durBind(func(c *Config) *time.Duration { return &c.InputTimeout })},
	{Env: "LIVE_STABLE_TIME", Label: "Live stable time", Group: "Switching", Kind: kindDuration, Editable: true, Applies: appliesNow,
		Default: "3s", Help: "Data must flow this long before switching from fallback back to live.",
		bind: durBind(func(c *Config) *time.Duration { return &c.LiveStableTime })},
	{Env: "FALLBACK_MAX_DURATION", Label: "Max fallback time", Group: "Switching", Kind: kindDuration, Editable: true, Applies: appliesNow,
		Default: "0", Help: "End the stream when the phone has been gone this long. 0 = never.",
		bind: durBind(func(c *Config) *time.Duration { return &c.FallbackMaxDuration })},
	{Env: "FALLBACK_FILE", Label: "Fallback file", Group: "Switching", Kind: kindText, Applies: appliesEnvOnly,
		Default: "/media/fallback.mp4", bind: strBind(func(c *Config) *string { return &c.FallbackFile })},

	{Env: "PREVIEW_ENABLED", Label: "Input preview", Group: "Web UI", Kind: kindBool, Editable: true, Applies: appliesNow,
		Default: "true", Help: "Allow the web UI to show snapshots of the phone's stream (decodes one keyframe per second, only while someone watches).",
		bind: boolBind(func(c *Config) *bool { return &c.Preview })},
	{Env: "API_TOKEN", Label: "API token", Group: "Web UI", Kind: kindSecret, Applies: appliesEnvOnly,
		bind: strBind(func(c *Config) *string { return &c.APIToken })},
	{Env: "API_LISTEN_ADDR", Label: "Listen address", Group: "Web UI", Kind: kindText, Applies: appliesEnvOnly,
		Default: ":8080", bind: strBind(func(c *Config) *string { return &c.APIListenAddr })},
	{Env: "SETTINGS_FILE", Label: "Settings file", Group: "Web UI", Kind: kindText, Applies: appliesEnvOnly,
		Default: "/data/settings.json", Help: "Where changes made here are saved. They override .env.",
		bind: strBind(func(c *Config) *string { return &c.SettingsFile })},

	{Env: "FFMPEG_ANALYZE_DURATION", Label: "Analyze duration", Group: "Advanced", Kind: kindDuration, Editable: true, Applies: appliesNextProcess,
		Default: "1s", Help: "How much input ffmpeg analyses before connecting to Twitch.",
		bind: durBind(func(c *Config) *time.Duration { return &c.AnalyzeDuration })},
	{Env: "FFMPEG_LOGLEVEL", Label: "ffmpeg log level", Group: "Advanced", Kind: kindSelect, Editable: true, Applies: appliesNextProcess,
		Default: "warning", Options: []string{"quiet", "error", "warning", "info", "verbose", "debug"},
		bind: strBind(func(c *Config) *string { return &c.FFmpegLogLevel })},
	{Env: "LOG_LEVEL", Label: "Relay log level", Group: "Advanced", Kind: kindSelect, Editable: true, Applies: appliesNow,
		Default: "info", Options: []string{"debug", "info", "warn", "error"},
		bind: strBind(func(c *Config) *string { return &c.LogLevel })},
	{Env: "FFMPEG_PATH", Label: "ffmpeg binary", Group: "Advanced", Kind: kindText, Applies: appliesEnvOnly,
		Default: "ffmpeg", bind: strBind(func(c *Config) *string { return &c.FFmpegPath })},
}

func settingDefByEnv(key string) (settingDef, bool) {
	for _, d := range settingDefs {
		if d.Env == key {
			return d, true
		}
	}
	return settingDef{}, false
}

// LoadConfig reads the environment. A missing stream key is not an error
// here, because it can also be set from the web UI.
func LoadConfig() (Config, error) {
	var (
		c    Config
		errs []error
	)
	for _, d := range settingDefs {
		if err := d.bind.set(&c, env(d.Env, d.Default)); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", d.Env, err))
			_ = d.bind.set(&c, d.Default)
		}
	}
	if err := c.validate(); err != nil {
		errs = append(errs, err)
	}
	return c, errors.Join(errs...)
}

func (c *Config) validate() error {
	var errs []error
	c.RTMPURL = strings.TrimRight(c.RTMPURL, "/")
	if !strings.HasPrefix(c.RTMPURL, "rtmp://") && !strings.HasPrefix(c.RTMPURL, "rtmps://") {
		errs = append(errs, errors.New("TWITCH_RTMP_URL must start with rtmp:// or rtmps://"))
	}
	if n := len(c.SRTPassphrase); n != 0 && (n < 10 || n > 79) {
		errs = append(errs, errors.New("SRT_PASSPHRASE must be between 10 and 79 characters (SRT limitation)"))
	}
	if c.InputTimeout < 200*time.Millisecond {
		errs = append(errs, errors.New("INPUT_TIMEOUT must be at least 200ms"))
	}
	return errors.Join(errs...)
}

// OutputURL is the full RTMP publish URL including the stream key.
func (c Config) OutputURL() string {
	return c.RTMPURL + "/" + c.StreamKey
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

// binding parses a setting from and formats it to its environment variable
// syntax, which is also what the web UI and the settings file use.
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
		return fmt.Errorf("invalid duration %q (use e.g. 1500ms, 3s, 10m)", v)
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
