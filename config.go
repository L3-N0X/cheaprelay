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

	FFmpegPath      string
	FFmpegLogLevel  string
	AnalyzeDuration time.Duration
	LogLevel        string
}

func LoadConfig() (Config, error) {
	var errs []error

	c := Config{
		SRTListenAddr:  env("SRT_LISTEN_ADDR", ":9000"),
		SRTPassphrase:  env("SRT_PASSPHRASE", ""),
		SRTStreamID:    env("SRT_STREAM_ID", ""),
		RTMPURL:        strings.TrimRight(env("TWITCH_RTMP_URL", "rtmp://live.twitch.tv/app"), "/"),
		StreamKey:      env("TWITCH_STREAM_KEY", ""),
		FallbackFile:   env("FALLBACK_FILE", "/media/fallback.mp4"),
		APIListenAddr:  env("API_LISTEN_ADDR", ":8080"),
		APIToken:       env("API_TOKEN", ""),
		FFmpegPath:     env("FFMPEG_PATH", "ffmpeg"),
		FFmpegLogLevel: env("FFMPEG_LOGLEVEL", "warning"),
		LogLevel:       env("LOG_LEVEL", "info"),
	}

	c.SRTLatency = envDuration("SRT_LATENCY", 2*time.Second, &errs)
	c.InputTimeout = envDuration("INPUT_TIMEOUT", 1500*time.Millisecond, &errs)
	c.LiveStableTime = envDuration("LIVE_STABLE_TIME", 3*time.Second, &errs)
	c.FallbackMaxDuration = envDuration("FALLBACK_MAX_DURATION", 0, &errs)
	c.AnalyzeDuration = envDuration("FFMPEG_ANALYZE_DURATION", time.Second, &errs)
	c.Autostart = envBool("AUTOSTART", false, &errs)

	if c.StreamKey == "" {
		errs = append(errs, errors.New("TWITCH_STREAM_KEY is required"))
	}
	if n := len(c.SRTPassphrase); n != 0 && (n < 10 || n > 79) {
		errs = append(errs, errors.New("SRT_PASSPHRASE must be between 10 and 79 characters (SRT limitation)"))
	}
	if c.InputTimeout < 200*time.Millisecond {
		errs = append(errs, errors.New("INPUT_TIMEOUT must be at least 200ms"))
	}

	return c, errors.Join(errs...)
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

func envDuration(key string, def time.Duration, errs *[]error) time.Duration {
	v := env(key, "")
	if v == "" {
		return def
	}
	if v == "0" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		*errs = append(*errs, fmt.Errorf("%s: invalid duration %q (use e.g. 1500ms, 3s, 10m)", key, v))
		return def
	}
	return d
}

func envBool(key string, def bool, errs *[]error) bool {
	v := env(key, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: invalid boolean %q", key, v))
		return def
	}
	return b
}
