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
	cfg          Config
	log          *slog.Logger
	in           *Ingest
	fallbackFile string

	cmds     chan command
	loopDone chan struct{}

	// owned by the run loop
	wantRunning   bool
	forceFallback bool
	proc          *ffmpegProc
	procSession   uint64
	mode          Mode
	modeSince     time.Time
	failures      int
	retryAt       time.Time
	switches      int
	restarts      int
	lastError     string
	lastErrorAt   time.Time
	hevcSession   uint64 // SRT session the H.265 error was last reported for

	statusMu sync.Mutex
	status   OutputStatus
}

type command struct {
	apply func()
	done  chan struct{} // closed once the change has been reconciled
}

type OutputStatus struct {
	Running        bool      `json:"running"`
	Mode           Mode      `json:"mode"`
	ModeSince      time.Time `json:"mode_since"`
	ForcedFallback bool      `json:"forced_fallback"`
	FFmpegRunning  bool      `json:"ffmpeg_running"`
	FFmpegPID      int       `json:"ffmpeg_pid,omitempty"`
	Switches       int       `json:"switches"`
	Restarts       int       `json:"restarts"`
	LastError      string    `json:"last_error,omitempty"`
	LastErrorAt    time.Time `json:"last_error_at,omitzero"`
}

var errShuttingDown = errors.New("relay is shutting down")

func NewSupervisor(cfg Config, in *Ingest, fallbackFile string, log *slog.Logger) *Supervisor {
	return &Supervisor{
		cfg:          cfg,
		log:          log.With("component", "supervisor"),
		in:           in,
		fallbackFile: fallbackFile,
		cmds:         make(chan command),
		loopDone:     make(chan struct{}),
		mode:         ModeStopped,
		modeSince:    time.Now(),
	}
}

func (s *Supervisor) Run(ctx context.Context) {
	defer close(s.loopDone)

	if s.cfg.Autostart {
		s.log.Info("AUTOSTART enabled, starting stream")
		s.wantRunning = true
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
			s.setMode(ModeStopped)
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

	if !s.wantRunning {
		if s.proc != nil {
			s.stopProc()
		}
		s.setMode(ModeStopped)
		return
	}

	if s.proc == nil && now.Before(s.retryAt) {
		return
	}

	if s.cfg.FallbackMaxDuration > 0 && !s.forceFallback && s.mode == ModeFallback &&
		!in.Active && now.Sub(s.modeSince) >= s.cfg.FallbackMaxDuration {
		s.log.Warn("no input for FALLBACK_MAX_DURATION, ending the stream", "after", s.cfg.FallbackMaxDuration)
		s.wantRunning = false
		if s.proc != nil {
			s.stopProc()
		}
		s.setMode(ModeStopped)
		return
	}

	if in.VideoCodec == codecHEVC && in.SessionID != s.hevcSession {
		s.hevcSession = in.SessionID
		s.lastError = "phone sends H.265/HEVC, Twitch only accepts H.264: set the video codec in the phone app to H.264"
		s.lastErrorAt = now
	}

	want := s.desiredMode(in, now)

	if s.proc != nil && s.proc.mode == want {
		if want == ModeLive && in.Active && in.SessionID != s.procSession {
			s.log.Info("phone reconnected (new SRT connection), reconnecting live output")
			s.killProc()
			s.switches++
			s.startProc(ModeLive)
		}
		return
	}

	if s.proc != nil {
		s.log.Info("switching output", "from", s.proc.mode, "to", want,
			"input_active", in.Active, "forced_fallback", s.forceFallback)
		s.killProc()
		s.switches++
	}
	s.startProc(want)
}

func (s *Supervisor) desiredMode(in InputState, now time.Time) Mode {
	switch {
	case s.forceFallback || !in.Active:
		return ModeFallback
	case in.VideoCodec == codecHEVC:
		// Twitch drops the connection or shows a broken player for H.265.
		return ModeFallback
	case s.mode == ModeLive:
		return ModeLive
	case s.proc == nil && s.mode == ModeStopped:
		// Fresh start with the phone already sending: nothing to interrupt.
		return ModeLive
	case now.Sub(in.ActiveSince) >= s.cfg.LiveStableTime:
		// Back from fallback only once the input has been stable for a while,
		// so a flapping connection doesn't cause a stall every few seconds.
		return ModeLive
	default:
		return ModeFallback
	}
}

func (s *Supervisor) startProc(mode Mode) {
	p, err := startFFmpeg(s.cfg, mode, s.fallbackFile, s.log)
	if err != nil {
		s.fail(fmt.Sprintf("start ffmpeg (%s): %v", mode, err))
		return
	}
	if mode == ModeLive {
		id, ok := s.in.Attach(p)
		if !ok {
			// The phone disconnected in the meantime; the next tick picks fallback.
			p.Kill()
			return
		}
		s.procSession = id
	}
	s.proc = p
	s.setMode(mode)
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
	s.fail(fmt.Sprintf("ffmpeg (%s) exited unexpectedly: %v", p.mode, err))
}

func (s *Supervisor) fail(msg string) {
	s.failures++
	backoff := min(time.Duration(1<<min(s.failures-1, 5))*500*time.Millisecond, maxRetryBackoff)
	s.retryAt = time.Now().Add(backoff)
	s.lastError = msg
	s.lastErrorAt = time.Now()
	s.log.Error(msg, "retry_in", backoff)
}

func (s *Supervisor) setMode(m Mode) {
	if m == s.mode {
		return
	}
	s.log.Info("output mode changed", "from", s.mode, "to", m)
	s.mode = m
	s.modeSince = time.Now()
}

func (s *Supervisor) publish() {
	st := OutputStatus{
		Running:        s.wantRunning,
		Mode:           s.mode,
		ModeSince:      s.modeSince,
		ForcedFallback: s.forceFallback,
		Switches:       s.switches,
		Restarts:       s.restarts,
		LastError:      s.lastError,
		LastErrorAt:    s.lastErrorAt,
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

func (s *Supervisor) Start(ctx context.Context) error {
	return s.do(ctx, func() {
		if !s.wantRunning {
			s.log.Info("start requested")
			s.wantRunning = true
			s.failures = 0
			s.retryAt = time.Time{}
		}
	})
}

func (s *Supervisor) Stop(ctx context.Context) error {
	return s.do(ctx, func() {
		if s.wantRunning {
			s.log.Info("stop requested")
			s.wantRunning = false
		}
	})
}

func (s *Supervisor) SetForceFallback(ctx context.Context, on bool) error {
	return s.do(ctx, func() {
		if s.forceFallback != on {
			s.log.Info("forced fallback changed", "enabled", on)
			s.forceFallback = on
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
