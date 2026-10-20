package main

import (
	"bytes"
	"context"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	previewTick      = 250 * time.Millisecond
	previewIdleAfter = 5 * time.Second        // stop decoding when nobody asked for this long
	previewMinGOPAge = 300 * time.Millisecond // let the keyframe arrive completely
	previewTimeout   = 3 * time.Second
)

// Preview decodes the latest keyframe of the phone's stream into a small JPEG
// for the web UI, and measures the audio level of the data since then. Only
// keyframes are decoded, and only while someone is watching, so it costs very
// little CPU (≈100 ms of one core per keyframe at 1080p).
type Preview struct {
	settings *Settings
	in       *Ingest
	log      *slog.Logger

	lastWanted atomic.Int64 // unix nanos of the last request

	mu    sync.Mutex
	frame PreviewFrame
}

// PreviewFrame is the latest snapshot. JPEG is not part of the status JSON.
type PreviewFrame struct {
	Seq         uint64    `json:"seq"`
	At          time.Time `json:"at,omitzero"`
	Width       int       `json:"width,omitempty"`
	Height      int       `json:"height,omitempty"`
	FPS         float64   `json:"fps,omitempty"`
	AudioMeanDB *float64  `json:"audio_mean_db"`
	AudioPeakDB *float64  `json:"audio_peak_db"`
	JPEG        []byte    `json:"-"`

	session, keyframe uint64
}

func NewPreview(settings *Settings, in *Ingest, log *slog.Logger) *Preview {
	return &Preview{settings: settings, in: in, log: log.With("component", "preview")}
}

// Want marks that a client is watching the preview.
func (p *Preview) Want() { p.lastWanted.Store(time.Now().UnixNano()) }

func (p *Preview) Latest() PreviewFrame {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.frame
}

func (p *Preview) Run(ctx context.Context) {
	t := time.NewTicker(previewTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !p.settings.Get().Preview || time.Since(time.Unix(0, p.lastWanted.Load())) > previewIdleAfter {
			continue
		}
		snap, ok := p.in.KeyframeSnapshot()
		if !ok || snap.age < previewMinGOPAge {
			continue
		}
		cur := p.Latest()
		if cur.session == snap.session && cur.keyframe == snap.keyframe {
			continue
		}
		f, err := p.decode(ctx, snap)
		if err != nil {
			if ctx.Err() == nil {
				p.log.Debug("preview failed", "err", err)
			}
			// Don't retry the same keyframe over and over.
			f = cur
			f.session, f.keyframe = snap.session, snap.keyframe
		}
		p.mu.Lock()
		f.Seq = p.frame.Seq
		if err == nil {
			f.Seq++
		}
		p.frame = f
		p.mu.Unlock()
	}
}

var (
	reVideo = regexp.MustCompile(`Video: .*?, (\d{2,5})x(\d{2,5})`)
	reFPS   = regexp.MustCompile(`, ([\d.]+) fps`)
	reMean  = regexp.MustCompile(`mean_volume: (-?[\d.]+) dB`)
	reMax   = regexp.MustCompile(`max_volume: (-?[\d.]+) dB`)
)

func (p *Preview) decode(ctx context.Context, snap keyframeSnapshot) (PreviewFrame, error) {
	ctx, cancel := context.WithTimeout(ctx, previewTimeout)
	defer cancel()

	args := []string{"-hide_banner", "-nostdin", "-nostats", "-loglevel", "info", "-threads", "1",
		"-skip_frame", "nokey", "-f", "mpegts", "-i", "pipe:0",
		"-map", "0:v:0", "-frames:v", "1",
		"-vf", "scale=w=960:h=540:force_original_aspect_ratio=decrease:force_divisible_by=2",
		"-q:v", "5", "-f", "image2pipe", "-c:v", "mjpeg", "pipe:1"}
	if snap.hasAudio {
		args = append(args, "-map", "0:a:0", "-vn", "-af", "volumedetect", "-f", "null", "-")
	}
	cmd := exec.CommandContext(ctx, p.settings.Get().FFmpegPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdin = bytes.NewReader(snap.data)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil && stdout.Len() == 0 {
		return PreviewFrame{}, err
	}

	f := PreviewFrame{At: time.Now(), JPEG: stdout.Bytes(), session: snap.session, keyframe: snap.keyframe}
	log := stderr.Bytes()
	if m := reVideo.FindSubmatch(log); m != nil {
		f.Width, _ = strconv.Atoi(string(m[1]))
		f.Height, _ = strconv.Atoi(string(m[2]))
	}
	if m := reFPS.FindSubmatch(log); m != nil {
		f.FPS, _ = strconv.ParseFloat(string(m[1]), 64)
	}
	f.AudioMeanDB = parseDB(reMean, log)
	f.AudioPeakDB = parseDB(reMax, log)
	return f, nil
}

func parseDB(re *regexp.Regexp, b []byte) *float64 {
	m := re.FindSubmatch(b)
	if m == nil {
		return nil
	}
	v, err := strconv.ParseFloat(string(m[1]), 64)
	if err != nil {
		return nil
	}
	return &v
}
