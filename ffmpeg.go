package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Mode string

const (
	ModeStopped  Mode = "stopped"
	ModeLive     Mode = "live"
	ModeFallback Mode = "fallback"
)

// ffmpegProc is one ffmpeg process holding one RTMP connection to Twitch.
//
// Kill() terminates it with SIGKILL: no FCUnpublish/deleteStream is sent, so
// Twitch treats it as a lost connection and accepts a reconnect without
// ending the broadcast. Stop() terminates it gracefully, which ends the
// broadcast immediately.
type ffmpegProc struct {
	mode      Mode
	startedAt time.Time
	log       *slog.Logger

	cmd   *exec.Cmd
	pipeW *os.File    // live only: MPEG-TS input, ffmpeg reads it as fd 3
	queue chan []byte // live only: chunks waiting to be written to pipeW

	done    chan struct{}
	exitErr error

	killed    atomic.Bool
	dropped   atomic.Uint64
	closeOnce sync.Once
}

func startFFmpeg(cfg Config, mode Mode, fallbackFile string, log *slog.Logger) (*ffmpegProc, error) {
	args := []string{"-hide_banner", "-nostdin", "-nostats", "-loglevel", cfg.FFmpegLogLevel}
	switch mode {
	case ModeLive:
		// ffmpeg's default analyzeduration (5s) would delay every switch to
		// live by ~5s. The keyframe cache delivers a full GOP right away, so a
		// short analysis window is enough.
		args = append(args,
			"-analyzeduration", strconv.FormatInt(cfg.AnalyzeDuration.Microseconds(), 10),
			"-f", "mpegts", "-i", "pipe:3")
	case ModeFallback:
		args = append(args, "-re", "-stream_loop", "-1", "-i", fallbackFile)
	default:
		return nil, fmt.Errorf("invalid mode %q", mode)
	}
	args = append(args,
		"-map", "0:v:0", "-map", "0:a:0?",
		"-c", "copy",
		"-f", "flv", "-flvflags", "no_duration_filesize",
		cfg.OutputURL(),
	)

	p := &ffmpegProc{
		mode:      mode,
		startedAt: time.Now(),
		log:       log.With("ffmpeg", mode),
		cmd:       exec.Command(cfg.FFmpegPath, args...),
		done:      make(chan struct{}),
	}

	// exec copies stderr into this pipe and Wait() waits for that copy, so
	// ffmpeg's last error lines are never lost.
	stderrR, stderrW := io.Pipe()
	p.cmd.Stderr = stderrW

	var (
		pipeR *os.File
		err   error
	)
	if mode == ModeLive {
		pipeR, p.pipeW, err = os.Pipe()
		if err != nil {
			return nil, err
		}
		p.cmd.ExtraFiles = []*os.File{pipeR} // becomes fd 3 in the child
		p.queue = make(chan []byte, 4096)
	}

	if err := p.cmd.Start(); err != nil {
		if pipeR != nil {
			pipeR.Close()
			p.pipeW.Close()
		}
		stderrW.Close()
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}
	if pipeR != nil {
		pipeR.Close() // the child owns the read end now
		go p.writeLoop()
	}

	go p.logStderr(stderrR)
	go func() {
		p.exitErr = p.cmd.Wait()
		stderrW.Close()
		p.closeInput()
		close(p.done)
	}()

	p.log.Info("ffmpeg started", "pid", p.cmd.Process.Pid)
	return p, nil
}

// Feed queues MPEG-TS data for the live process. It never blocks; if ffmpeg
// cannot keep up the data is dropped.
func (p *ffmpegProc) Feed(b []byte) {
	if p.queue == nil {
		return
	}
	select {
	case <-p.done:
	case p.queue <- b:
	default:
		if p.dropped.Add(1)%100 == 1 {
			p.log.Warn("ffmpeg input queue full, dropping data", "dropped_chunks", p.dropped.Load())
		}
	}
}

func (p *ffmpegProc) writeLoop() {
	for {
		select {
		case <-p.done:
			return
		case b := <-p.queue:
			if _, err := p.pipeW.Write(b); err != nil {
				return
			}
		}
	}
}

func (p *ffmpegProc) closeInput() {
	p.closeOnce.Do(func() {
		if p.pipeW != nil {
			p.pipeW.Close()
		}
	})
}

func (p *ffmpegProc) Done() <-chan struct{} { return p.done }

// Exited reports whether the process ended and the error it ended with.
func (p *ffmpegProc) Exited() (bool, error) {
	select {
	case <-p.done:
		return true, p.exitErr
	default:
		return false, nil
	}
}

// WasKilled reports whether the process was ended by Kill or Stop (as opposed
// to exiting on its own).
func (p *ffmpegProc) WasKilled() bool { return p.killed.Load() }

// Kill drops the RTMP connection without unpublishing (SIGKILL).
func (p *ffmpegProc) Kill() {
	p.killed.Store(true)
	_ = p.cmd.Process.Kill()
	<-p.done
	p.log.Info("ffmpeg killed (connection dropped without unpublish)")
}

// Stop ends the stream gracefully: ffmpeg unpublishes and Twitch ends the
// broadcast right away.
func (p *ffmpegProc) Stop(timeout time.Duration) {
	p.killed.Store(true)
	if p.pipeW != nil {
		// ffmpeg ignores SIGINT while it reads from the pipe (it stops muxing
		// but never exits). End of input makes it finish normally instead.
		p.closeInput()
	} else {
		_ = p.cmd.Process.Signal(os.Interrupt)
	}
	select {
	case <-p.done:
		p.log.Info("ffmpeg stopped gracefully (stream unpublished)")
	case <-time.After(timeout):
		_ = p.cmd.Process.Kill()
		<-p.done
		p.log.Warn("ffmpeg did not stop in time, killed")
	}
}

func (p *ffmpegProc) logStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			p.log.Info(line)
		}
	}
	_, _ = io.Copy(io.Discard, r) // never block ffmpeg if scanning stopped early
}

// ensureFallbackFile returns a usable fallback file. If the configured file is
// missing, a plain dark screen with silence is generated so the relay still
// works out of the box.
func ensureFallbackFile(cfg Config, log *slog.Logger) (string, error) {
	out := os.TempDir() + "/default-fallback.mp4"
	st, err := os.Stat(cfg.FallbackFile)
	switch {
	case err == nil && !st.IsDir():
		checkFallbackFile(cfg, cfg.FallbackFile, log)
		return cfg.FallbackFile, nil
	case err == nil:
		// Typically "rclone copy video.mp4 remote:media/fallback.mp4", which
		// creates a directory named fallback.mp4 with the video inside.
		log.Warn("fallback path is a directory, not a file (use \"rclone copyto\" or \"scp\" to upload a single file); generating a plain dark screen instead",
			"configured", cfg.FallbackFile, "generated", out)
	case errors.Is(err, os.ErrNotExist):
		log.Warn("fallback file not found, generating a plain dark screen instead",
			"configured", cfg.FallbackFile, "generated", out)
	default:
		return "", fmt.Errorf("fallback file: %w", err)
	}

	cmd := exec.Command(cfg.FFmpegPath, "-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=0x18181b:s=1280x720:r=30",
		"-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo",
		"-t", "4",
		"-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p", "-g", "60", "-bf", "0", "-b:v", "500k",
		"-c:a", "aac", "-b:a", "128k",
		out,
	)
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("generate default fallback: %w: %s", err, b)
	}
	return out, nil
}

// checkFallbackFile warns if the fallback clip is likely to be rejected by
// Twitch. It is streamed with -c copy, so it has to be prepared with
// make-fallback (H.264 + AAC, at most 1080p).
func checkFallbackFile(cfg Config, path string, log *slog.Logger) {
	ffprobe := "ffprobe"
	if dir := filepath.Dir(cfg.FFmpegPath); dir != "." {
		ffprobe = filepath.Join(dir, "ffprobe")
	}
	b, err := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_type,codec_name,height",
		"-of", "csv=p=0", path).Output()
	if err != nil {
		log.Warn("could not inspect the fallback file", "path", path, "err", err)
		return
	}
	var video, audio string
	height := 0
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Split(strings.TrimSpace(line), ",")
		switch {
		case len(f) >= 3 && f[1] == "video" && video == "":
			video = f[0]
			height, _ = strconv.Atoi(f[2])
		case len(f) >= 2 && f[1] == "audio" && audio == "":
			audio = f[0]
		}
	}
	var problems []string
	if video != "h264" {
		problems = append(problems, fmt.Sprintf("video codec is %q, Twitch needs h264", video))
	}
	if height > 1080 {
		problems = append(problems, fmt.Sprintf("resolution is %dp, Twitch accepts at most 1080p", height))
	}
	if audio == "" {
		problems = append(problems, "no audio track")
	} else if audio != "aac" {
		problems = append(problems, fmt.Sprintf("audio codec is %q, Twitch needs aac", audio))
	}
	if len(problems) > 0 {
		log.Warn("fallback file is not prepared for Twitch, convert it with make-fallback (see README)",
			"path", path, "problems", strings.Join(problems, "; "))
	}
}
