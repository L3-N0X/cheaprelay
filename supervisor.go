package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	tickInterval     = 100 * time.Millisecond
	gracefulTimeout  = 5 * time.Second
	maxRetryBackoff  = 10 * time.Second
	stableRunTimeout = 30 * time.Second // a process that ran this long resets the backoff
)

// Supervisor owns the ffmpeg processes and decides which one should run.
// All state below "owned by the run loop" is only touched by Run; the API
// talks to it through do().
type Supervisor struct {
	settings *Settings
	log      *slog.Logger
	in       *Ingest
	scenes   []PreparedScene // fixed at startup

	cmds     chan command
	loopDone chan struct{}

	// owned by the run loop
	wantRunning bool
	pickedScene string // scene picked by hand, shown even if the phone is fine; "" = automatic
	proc        *ffmpegProc
	procSession uint64
	out         output
	modeSince   time.Time
	failures    int
	failed      output // output of the last failed ffmpeg; retryAt only delays this one
	retryAt     time.Time
	switches    int
	restarts    int
	lastError   string
	lastErrorAt time.Time
	hevcSession uint64 // SRT session the H.265 error was last reported for
	startedAt   time.Time

	statusMu sync.Mutex
	status   OutputStatus
}

type command struct {
	apply func()
	done  chan struct{} // closed once the change has been reconciled
}

// output is what is streamed to Twitch: the phone, a scene or nothing.
type output struct {
	mode  Mode
	scene string // fallback only
}

func (o output) String() string {
	if o.scene != "" {
		return string(o.mode) + " (" + o.scene + ")"
	}
	return string(o.mode)
}

type OutputStatus struct {
	Running       bool      `json:"running"`
	StartedAt     time.Time `json:"started_at,omitzero"` // when the broadcast was started
	Mode          Mode      `json:"mode"`
	Scene         string    `json:"scene,omitempty"` // the scene on air in fallback mode
	ModeSince     time.Time `json:"mode_since"`
	PickedScene   string    `json:"picked_scene,omitempty"` // "" = automatic
	FFmpegRunning bool      `json:"ffmpeg_running"`
	FFmpegPID     int       `json:"ffmpeg_pid,omitempty"`
	Switches      int       `json:"switches"`
	Restarts      int       `json:"restarts"`
	LastError     string    `json:"last_error,omitempty"`
	LastErrorAt   time.Time `json:"last_error_at,omitzero"`
}

var (
	errShuttingDown = errors.New("relay is shutting down")
	errUnknownScene = errors.New("unknown scene")
)

func NewSupervisor(settings *Settings, in *Ingest, scenes []PreparedScene, log *slog.Logger) *Supervisor {
	return &Supervisor{
		settings:  settings,
		log:       log.With("component", "supervisor"),
		in:        in,
		scenes:    scenes,
		cmds:      make(chan command),
		loopDone:  make(chan struct{}),
		out:       output{mode: ModeStopped},
		modeSince: time.Now(),
	}
}

// Scenes returns the scenes with their files. They don't change at runtime.
func (s *Supervisor) Scenes() []PreparedScene { return s.scenes }

func (s *Supervisor) scene(id string) *PreparedScene {
	for i := range s.scenes {
		if s.scenes[i].ID == id {
			return &s.scenes[i]
		}
	}
	return nil
}

func (s *Supervisor) Run(ctx context.Context) {
	defer close(s.loopDone)

	if cfg := s.settings.Get(); cfg.Autostart {
		if cfg.StreamKey == "" {
			s.log.Warn("AUTOSTART is enabled, but no stream key is set: not starting")
		} else {
			s.log.Info("AUTOSTART enabled, starting stream")
			s.wantRunning = true
		}
	}

	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for {
		var (
			procDone <-chan struct{}
			cmdDone  chan struct{}
		)
		if s.proc != nil {
			procDone = s.proc.Done()
		}

		select {
		case <-ctx.Done():
			if s.proc != nil {
				s.log.Info("shutting down, ending stream")
				s.stopProc()
			}
			s.setOutput(output{mode: ModeStopped})
			s.publish()
			return
		case c := <-s.cmds:
			c.apply()
			cmdDone = c.done
		case <-procDone:
			s.handleUnexpectedExit()
		case <-t.C:
		}

		s.reconcile(time.Now())
		s.publish()
		if cmdDone != nil {
			close(cmdDone)
		}
	}
}

// reconcile starts, stops or switches ffmpeg so that the output matches the
// desired state.
func (s *Supervisor) reconcile(now time.Time) {
	in := s.in.Snapshot()
	cfg := s.settings.Get()

	if !s.wantRunning {
		if s.proc != nil {
			s.stopProc()
		}
		s.setOutput(output{mode: ModeStopped})
		return
	}

	if cfg.FallbackMaxDuration > 0 && s.pickedScene == "" && s.out.mode == ModeFallback &&
		!in.Active && now.Sub(s.modeSince) >= cfg.FallbackMaxDuration {
		s.log.Warn("no input for switching.fallback_max_duration, ending the stream", "after", cfg.FallbackMaxDuration)
		s.wantRunning = false
		if s.proc != nil {
			s.stopProc()
		}
		s.setOutput(output{mode: ModeStopped})
		return
	}

	if in.VideoCodec == codecHEVC && in.SessionID != s.hevcSession {
		s.hevcSession = in.SessionID
		s.lastError = "phone sends H.265/HEVC, Twitch only accepts H.264: set the video codec in the phone app to H.264"
		s.lastErrorAt = now
	}

	want := output{mode: s.desiredMode(in, cfg.LiveStableTime, now)}
	if want.mode == ModeFallback {
		want.scene = cfg.FallbackScene
		if s.pickedScene != "" {
			want.scene = s.pickedScene
		}
	}

	// The retry delay only holds back the output that failed. Switching to
	// another one (e.g. back to live while the fallback keeps failing, or to
	// another scene) happens right away.
	if s.proc == nil && want == s.failed && now.Before(s.retryAt) {
		return
	}

	if s.proc != nil && procOutput(s.proc) == want {
		if want.mode == ModeLive && in.Active && in.SessionID != s.procSession {
			s.log.Info("phone reconnected (new SRT connection), reconnecting live output")
			s.killProc()
			s.switches++
			s.startProc(output{mode: ModeLive})
		}
		return
	}

	if s.proc != nil {
		s.log.Info("switching output", "from", procOutput(s.proc), "to", want,
			"input_active", in.Active, "picked_scene", s.pickedScene)
		s.killProc()
		s.switches++
	}
	s.startProc(want)
}

func procOutput(p *ffmpegProc) output { return output{mode: p.mode, scene: p.scene} }

func (s *Supervisor) desiredMode(in InputState, stableTime time.Duration, now time.Time) Mode {
	switch {
	case s.pickedScene != "" || !in.Active:
		return ModeFallback
	case in.VideoCodec == codecHEVC:
		// Twitch drops the connection or shows a broken player for H.265.
		return ModeFallback
	case s.out.mode == ModeLive:
		return ModeLive
	case s.proc == nil && s.out.mode == ModeStopped:
		// Fresh start with the phone already sending: nothing to interrupt.
		return ModeLive
	case now.Sub(in.ActiveSince) >= stableTime:
		// Back from fallback only once the input has been stable for a while,
		// so a flapping connection doesn't cause a stall every few seconds.
		return ModeLive
	default:
		return ModeFallback
	}
}

func (s *Supervisor) startProc(o output) {
	p, err := startFFmpeg(s.settings.Get(), o.mode, s.scene(o.scene), s.log)
	if err != nil {
		s.fail(o, fmt.Sprintf("start ffmpeg (%s): %v", o, err))
		return
	}
	if o.mode == ModeLive {
		id, ok := s.in.Attach(p)
		if !ok {
			// The phone disconnected in the meantime; the next tick picks fallback.
			p.Kill()
			return
		}
		s.procSession = id
	}
	s.proc = p
	s.setOutput(o)
}

// killProc drops the Twitch connection without unpublishing.
func (s *Supervisor) killProc() {
	s.in.Detach(s.proc)
	s.proc.Kill()
	s.proc = nil
}

// stopProc unpublishes, which ends the Twitch broadcast.
func (s *Supervisor) stopProc() {
	s.in.Detach(s.proc)
	s.proc.Stop(gracefulTimeout)
	s.proc = nil
}

func (s *Supervisor) handleUnexpectedExit() {
	p := s.proc
	s.proc = nil
	s.in.Detach(p)
	_, err := p.Exited()
	if time.Since(p.startedAt) > stableRunTimeout {
		s.failures = 0
	}
	s.restarts++
	s.fail(procOutput(p), fmt.Sprintf("ffmpeg (%s) exited unexpectedly: %v", procOutput(p), err))
}

func (s *Supervisor) fail(o output, msg string) {
	if o != s.failed {
		s.failures = 0
	}
	s.failed = o
	s.failures++
	backoff := min(time.Duration(1<<min(s.failures-1, 5))*500*time.Millisecond, maxRetryBackoff)
	s.retryAt = time.Now().Add(backoff)
	s.lastError = msg
	s.lastErrorAt = time.Now()
	s.log.Error(msg, "retry_in", backoff)
}

func (s *Supervisor) setOutput(o output) {
	if o == s.out {
		return
	}
	s.log.Info("output changed", "from", s.out, "to", o)
	s.out = o
	s.modeSince = time.Now()
}

func (s *Supervisor) publish() {
	switch {
	case !s.wantRunning:
		s.startedAt = time.Time{}
	case s.startedAt.IsZero():
		s.startedAt = time.Now()
	}
	st := OutputStatus{
		Running:     s.wantRunning,
		StartedAt:   s.startedAt,
		Mode:        s.out.mode,
		Scene:       s.out.scene,
		ModeSince:   s.modeSince,
		PickedScene: s.pickedScene,
		Switches:    s.switches,
		Restarts:    s.restarts,
		LastError:   s.lastError,
		LastErrorAt: s.lastErrorAt,
	}
	if s.proc != nil {
		st.FFmpegRunning = true
		st.FFmpegPID = s.proc.cmd.Process.Pid
	}
	s.statusMu.Lock()
	s.status = st
	s.statusMu.Unlock()
}

func (s *Supervisor) Status() OutputStatus {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	return s.status
}

// do runs f on the supervisor loop and waits until the resulting state has
// been applied.
func (s *Supervisor) do(ctx context.Context, f func()) error {
	c := command{apply: f, done: make(chan struct{})}
	select {
	case s.cmds <- c:
	case <-s.loopDone:
		return errShuttingDown
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-c.done:
		return nil
	case <-s.loopDone:
		return errShuttingDown
	}
}

var errNoStreamKey = errors.New("no Twitch stream key set: add it in the settings first")

func (s *Supervisor) Start(ctx context.Context) error {
	if s.settings.Get().StreamKey == "" {
		return errNoStreamKey
	}
	return s.do(ctx, func() {
		if !s.wantRunning {
			s.log.Info("start requested")
			s.wantRunning = true
			s.failures = 0
			s.retryAt = time.Time{}
		}
	})
}

// Stop ends the broadcast. A picked scene (typically an "ending" screen) is
// reset, so the next stream doesn't start with it.
func (s *Supervisor) Stop(ctx context.Context) error {
	return s.do(ctx, func() {
		if s.wantRunning {
			s.log.Info("stop requested")
			s.wantRunning = false
			s.pickedScene = ""
		}
	})
}

// PickScene shows a scene even if the phone is fine. "" goes back to
// automatic switching. Picking a scene before going live makes the stream
// start with it.
func (s *Supervisor) PickScene(ctx context.Context, id string) error {
	if id != "" && s.scene(id) == nil {
		return fmt.Errorf("%w %q", errUnknownScene, id)
	}
	return s.do(ctx, func() {
		if s.pickedScene != id {
			s.log.Info("scene picked", "scene", id, "automatic", id == "")
			s.pickedScene = id
		}
	})
}

// Reconnect drops and re-establishes the current Twitch connection.
func (s *Supervisor) Reconnect(ctx context.Context) error {
	return s.do(ctx, func() {
		if s.proc != nil {
			s.log.Info("reconnect requested")
			s.killProc()
			s.switches++
		}
		s.retryAt = time.Time{}
	})
}
