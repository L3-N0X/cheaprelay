// srt-twitch-relay receives an SRT stream (e.g. from a phone) and forwards it
// to Twitch without re-encoding. When the input drops, a fallback video is
// streamed instead. Switching works by dropping the RTMP connection without
// unpublishing and reconnecting, which Twitch tolerates without ending the
// broadcast.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	cfg, err := LoadConfig()
	var level slog.LevelVar
	level.Set(parseLevel(cfg.LogLevel))
	logs := &LogRing{}
	log := slog.New(&ringHandler{
		next:  slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: &level}),
		ring:  logs,
		level: &level,
	})
	if err != nil {
		log.Error("invalid configuration", "err", err)
		os.Exit(2)
	}

	settings := NewSettings(cfg, log)
	settings.Load()
	settings.OnChange = func(c Config) { level.Set(parseLevel(c.LogLevel)) }
	cfg = settings.Get()
	level.Set(parseLevel(cfg.LogLevel))
	if cfg.StreamKey == "" {
		log.Warn("TWITCH_STREAM_KEY is not set: set it in .env or in the web UI before starting the stream")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	fallback, err := ensureFallbackFile(cfg, log)
	if err != nil {
		log.Error("no usable fallback file", "err", err)
		os.Exit(1)
	}
	log.Info("using fallback file", "path", fallback)

	in := NewIngest(settings, log)
	sup := NewSupervisor(settings, in, fallback, log)
	preview := NewPreview(settings, in, log)
	api := NewAPI(settings, sup, in, preview, logs, log)

	srv := &http.Server{
		Addr:              cfg.APIListenAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if cfg.APIToken == "" {
		log.Warn("API_TOKEN is not set: anyone who can reach the API can control the stream")
	}

	errc := make(chan error, 2)
	go func() { errc <- in.Run(ctx) }()
	go preview.Run(ctx)
	go func() {
		log.Info("API listening", "addr", cfg.APIListenAddr)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	supDone := make(chan struct{})
	go func() {
		sup.Run(ctx)
		close(supDone)
	}()

	exitCode := 0
	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err := <-errc:
		if err != nil {
			log.Error("fatal error", "err", err)
			exitCode = 1
		}
		cancel()
	}

	<-supDone // ends the stream gracefully
	shutdownCtx, c := context.WithTimeout(context.Background(), 3*time.Second)
	defer c()
	_ = srv.Shutdown(shutdownCtx)
	os.Exit(exitCode)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
