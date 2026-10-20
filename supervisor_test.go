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
	script := "#!/bin/sh\ncase \"$*\" in *stream_loop*) exit 1;; esac\nexec sleep 30\n"
	s, in := newTestSupervisor(t, script)

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
	phoneSending(in)
	s.reconcile(time.Now())
	if s.proc == nil || s.proc.mode != ModeLive {
		t.Fatalf("expected live ffmpeg right away, got %+v (mode %s)", s.proc, s.out)
	}
}

// newTestSupervisor returns a running supervisor with a fake ffmpeg (a shell
// script) and the scenes "brb" (the fallback) and "end".
func newTestSupervisor(t *testing.T, script string) (*Supervisor, *Ingest) {
	t.Helper()
	fake := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	scenes := []PreparedScene{
		{Scene: Scene{ID: "brb", Name: "BRB"}, Path: "brb.mp4"},
		{Scene: Scene{ID: "end", Name: "Ending"}, Path: "end.mp4"},
	}
	cfg := Config{FFmpegPath: fake, FFmpegLogLevel: "error", InputTimeout: time.Second, LiveStableTime: time.Second,
		FallbackScene: "brb"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	settings := newStaticSettings(cfg)
	in := NewIngest(settings, log)
	s := NewSupervisor(settings, in, scenes, log)
	s.wantRunning = true
	t.Cleanup(func() {
		if s.proc != nil {
			s.killProc()
		}
	})
	return s, in
}

// phoneSending makes the ingest look like the phone has been sending for 2s.
func phoneSending(in *Ingest) {
	in.mu.Lock()
	in.sess = &srtSession{id: 1}
	in.lastData = time.Now()
	in.activeSince = time.Now().Add(-2 * time.Second)
	in.mu.Unlock()
}

func TestPickScene(t *testing.T) {
	s, in := newTestSupervisor(t, "#!/bin/sh\nexec sleep 30\n")
	expect := func(want output) {
		t.Helper()
		if s.proc == nil || procOutput(s.proc) != want || s.out != want {
			t.Fatalf("expected %v, got proc %+v, output %v", want, s.proc, s.out)
		}
	}

	// No phone: the fallback scene.
	s.reconcile(time.Now())
	expect(output{mode: ModeFallback, scene: "brb"})

	phoneSending(in)
	s.reconcile(time.Now())
	expect(output{mode: ModeLive})

	// A picked scene wins over the phone, and switching scenes restarts ffmpeg.
	s.pickedScene = "end"
	s.reconcile(time.Now())
	expect(output{mode: ModeFallback, scene: "end"})
	s.pickedScene = "brb"
	s.reconcile(time.Now())
	expect(output{mode: ModeFallback, scene: "brb"})

	// Back to automatic: live again, the phone has been stable long enough.
	s.pickedScene = ""
	s.reconcile(time.Now())
	expect(output{mode: ModeLive})
	if s.switches != 4 {
		t.Errorf("switches = %d, want 4", s.switches)
	}
}
