package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	srt "github.com/datarhei/gosrt"
)

const gopCacheMaxBytes = 16 << 20

// Ingest accepts SRT publishers (the phone). Only one publisher is active at a
// time; a new connection replaces the previous one, which is what you want
// when the phone reconnects before the old connection has timed out.
type Ingest struct {
	cfg Config
	log *slog.Logger

	mu          sync.Mutex
	sess        *srtSession
	nextID      uint64
	lastData    time.Time
	activeSince time.Time
	cache       *gopCache
	sink        *ffmpegProc
	bytes       uint64
	bitrateKbps int
}

type srtSession struct {
	id          uint64
	conn        srt.Conn
	streamID    string
	remote      string
	connectedAt time.Time
	aligned     bool // payloads are whole TS packets (always true for sane senders)
}

// InputState is a point-in-time view of the SRT input.
type InputState struct {
	Connected   bool
	Active      bool // connected and data received within INPUT_TIMEOUT
	SessionID   uint64
	StreamID    string
	RemoteAddr  string
	ConnectedAt time.Time
	LastData    time.Time
	ActiveSince time.Time // start of the current uninterrupted data flow
	BitrateKbps int
	CacheBytes  int
	conn        srt.Conn
}

func NewIngest(cfg Config, log *slog.Logger) *Ingest {
	return &Ingest{cfg: cfg, log: log.With("component", "srt")}
}

func (in *Ingest) Run(ctx context.Context) error {
	conf := srt.DefaultConfig()
	conf.ReceiverLatency = in.cfg.SRTLatency
	conf.PeerLatency = in.cfg.SRTLatency

	ln, err := srt.Listen("srt", in.cfg.SRTListenAddr, conf)
	if err != nil {
		return fmt.Errorf("srt listen on %s: %w", in.cfg.SRTListenAddr, err)
	}
	in.log.Info("listening for SRT", "addr", in.cfg.SRTListenAddr,
		"passphrase", in.cfg.SRTPassphrase != "", "stream_id_required", in.cfg.SRTStreamID != "")

	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	go in.bitrateLoop(ctx)

	for {
		req, err := ln.Accept2()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, srt.ErrListenerClosed) {
				return nil
			}
			return fmt.Errorf("srt accept: %w", err)
		}
		in.handleRequest(req)
	}
}

func (in *Ingest) handleRequest(req srt.ConnRequest) {
	remote := req.RemoteAddr().String()
	streamID := req.StreamId()
	log := in.log.With("remote", remote, "stream_id", streamID)

	if want := in.cfg.SRTStreamID; want != "" && subtle.ConstantTimeCompare([]byte(streamID), []byte(want)) != 1 {
		log.Warn("rejected SRT connection: wrong stream id")
		req.Reject(srt.REJX_FORBIDDEN)
		return
	}
	if in.cfg.SRTPassphrase != "" {
		if !req.IsEncrypted() {
			log.Warn("rejected SRT connection: no passphrase set on the sender")
			req.Reject(srt.REJ_UNSECURE)
			return
		}
		if err := req.SetPassphrase(in.cfg.SRTPassphrase); err != nil {
			log.Warn("rejected SRT connection: wrong passphrase")
			req.Reject(srt.REJ_BADSECRET)
			return
		}
	} else if req.IsEncrypted() {
		log.Warn("rejected SRT connection: sender uses a passphrase but SRT_PASSPHRASE is not set")
		req.Reject(srt.REJ_UNSECURE)
		return
	}

	conn, err := req.Accept()
	if err != nil {
		log.Warn("failed to accept SRT connection", "err", err)
		return
	}
	go in.serve(conn, streamID, remote)
}

func (in *Ingest) serve(conn srt.Conn, streamID, remote string) {
	s := &srtSession{
		conn:        conn,
		streamID:    streamID,
		remote:      remote,
		connectedAt: time.Now(),
		aligned:     true,
	}

	in.mu.Lock()
	in.nextID++
	s.id = in.nextID
	old := in.sess
	in.sess = s
	in.cache = newGOPCache(gopCacheMaxBytes)
	in.activeSince = time.Time{}
	in.sink = nil // the supervisor re-attaches a fresh live ffmpeg for this session
	in.mu.Unlock()

	log := in.log.With("session", s.id, "remote", remote)
	log.Info("SRT publisher connected", "stream_id", streamID)
	if old != nil {
		log.Info("replacing previous SRT connection", "old_session", old.id)
		old.conn.Close()
	}

	buf := make([]byte, 2048)
	var readErr error
	for {
		n, err := conn.Read(buf)
		if err != nil {
			readErr = err
			break
		}
		if n > 0 {
			in.onData(s, buf[:n], log)
		}
	}
	conn.Close()

	in.mu.Lock()
	if in.sess == s {
		in.sess = nil
	}
	in.mu.Unlock()
	log.Info("SRT publisher disconnected", "reason", readErr, "duration", time.Since(s.connectedAt).Round(time.Second))
}

func (in *Ingest) onData(s *srtSession, b []byte, log *slog.Logger) {
	now := time.Now()

	in.mu.Lock()
	defer in.mu.Unlock()
	if in.sess != s {
		return
	}

	if in.activeSince.IsZero() || now.Sub(in.lastData) > in.cfg.InputTimeout {
		in.activeSince = now
	}
	in.lastData = now
	in.bytes += uint64(len(b))

	if s.aligned {
		if len(b)%tsPacketSize != 0 || b[0] != 0x47 {
			s.aligned = false
			in.cache = nil
			log.Warn("SRT payload is not aligned to MPEG-TS packets; keyframe cache disabled for this session")
		} else {
			for i := 0; i < len(b); i += tsPacketSize {
				in.cache.push(b[i : i+tsPacketSize])
			}
		}
	}

	if in.sink != nil {
		in.sink.Feed(append([]byte(nil), b...))
	}
}

// Attach connects a live ffmpeg to the current session. It first receives the
// cached data since the last keyframe, then everything that follows.
func (in *Ingest) Attach(p *ffmpegProc) (sessionID uint64, ok bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.sess == nil {
		return 0, false
	}
	if in.cache != nil {
		if snap := in.cache.snapshot(); snap != nil {
			p.Feed(snap)
		}
	}
	in.sink = p
	return in.sess.id, true
}

func (in *Ingest) Detach(p *ffmpegProc) {
	in.mu.Lock()
	if in.sink == p {
		in.sink = nil
	}
	in.mu.Unlock()
}

func (in *Ingest) Snapshot() InputState {
	in.mu.Lock()
	defer in.mu.Unlock()

	st := InputState{
		LastData:    in.lastData,
		ActiveSince: in.activeSince,
		BitrateKbps: in.bitrateKbps,
	}
	if in.cache != nil {
		st.CacheBytes = len(in.cache.gop)
	}
	if s := in.sess; s != nil {
		st.Connected = true
		st.SessionID = s.id
		st.StreamID = s.streamID
		st.RemoteAddr = s.remote
		st.ConnectedAt = s.connectedAt
		st.Active = !in.lastData.IsZero() && time.Since(in.lastData) < in.cfg.InputTimeout
		st.conn = s.conn
	}
	return st
}

func (in *Ingest) bitrateLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var prev uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			in.mu.Lock()
			in.bitrateKbps = int((in.bytes - prev) * 8 / 1000)
			prev = in.bytes
			in.mu.Unlock()
		}
	}
}
