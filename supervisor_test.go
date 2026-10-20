package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRetryDelayOnlyHoldsFailedMode: while the fallback keeps failing and
// waits for its retry, a phone that is back must still go live right away.
func TestRetryDelayOnlyHoldsFailedMode(t *testing.T) {
	// Fake ffmpeg: the fallback (file input with -stream_loop) fails at once,
	// live keeps running.
	fake := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\ncase \"$*\" in *stream_loop*) exit 1;; esac\nexec sleep 30\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{FFmpegPath: fake, FFmpegLogLevel: "error", InputTimeout: time.Second, LiveStableTime: time.Second}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	settings := newStaticSettings(cfg)
	in := NewIngest(settings, log)
	s := NewSupervisor(settings, in, "fallback.mp4", log)
	s.wantRunning = true
	defer func() {
		if s.proc != nil {
			s.killProc()
		}
	}()

	// No phone: the fallback starts and fails, which schedules a retry.
	now := time.Now()
	s.reconcile(now)
	if s.proc == nil || s.proc.mode != ModeFallback {
		t.Fatalf("expected fallback ffmpeg, got %+v", s.proc)
	}
	<-s.proc.Done()
	s.handleUnexpectedExit()
	if !time.Now().Before(s.retryAt) {
		t.Fatal("expected a pending retry")
	}

	// The phone is back and stable: live must start despite the pending retry.
	in.mu.Lock()
	in.sess = &srtSession{id: 1}
	in.lastData = time.Now()
	in.activeSince = time.Now().Add(-2 * time.Second)
	in.mu.Unlock()
	s.reconcile(time.Now())
	if s.proc == nil || s.proc.mode != ModeLive {
		t.Fatalf("expected live ffmpeg right away, got %+v (mode %s)", s.proc, s.mode)
	}
}
