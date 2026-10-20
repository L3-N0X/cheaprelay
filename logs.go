package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const logRingSize = 300

// LogEntry is one log record as shown in the web UI's activity feed.
type LogEntry struct {
	Seq       uint64    `json:"seq"`
	Time      time.Time `json:"time"`
	Level     string    `json:"level"`
	Component string    `json:"component,omitempty"`
	Message   string    `json:"msg"`
	Attrs     string    `json:"attrs,omitempty"`
}

// LogRing keeps the most recent log records in memory.
type LogRing struct {
	mu      sync.Mutex
	entries []LogEntry
	seq     uint64
}

func (r *LogRing) add(e LogEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	e.Seq = r.seq
	if len(r.entries) == logRingSize {
		r.entries = append(r.entries[:0], r.entries[1:]...)
	}
	r.entries = append(r.entries, e)
}

func (r *LogRing) Seq() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seq
}

// After returns the records with a sequence number greater than seq.
func (r *LogRing) After(seq uint64) []LogEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, e := range r.entries {
		if e.Seq > seq {
			return append([]LogEntry(nil), r.entries[i:]...)
		}
	}
	return []LogEntry{}
}

// ringHandler passes records on to the next handler and also records them in
// the ring, so the web UI can show what the relay is doing.
type ringHandler struct {
	next  slog.Handler
	ring  *LogRing
	level slog.Leveler
	attrs []slog.Attr
}

func (h *ringHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level.Level() }

func (h *ringHandler) Handle(ctx context.Context, r slog.Record) error {
	e := LogEntry{Time: r.Time, Level: strings.ToLower(r.Level.String()), Message: r.Message}
	var b strings.Builder
	add := func(a slog.Attr) {
		if a.Key == "component" {
			e.Component = a.Value.String()
			return
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s=%v", a.Key, a.Value)
	}
	for _, a := range h.attrs {
		add(a)
	}
	r.Attrs(func(a slog.Attr) bool { add(a); return true })
	e.Attrs = b.String()
	h.ring.add(e)
	return h.next.Handle(ctx, r)
}

func (h *ringHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &ringHandler{next: h.next.WithAttrs(as), ring: h.ring, level: h.level, attrs: append(h.attrs[:len(h.attrs):len(h.attrs)], as...)}
}

// WithGroup is not used by the relay; groups are flattened in the ring.
func (h *ringHandler) WithGroup(name string) slog.Handler {
	return &ringHandler{next: h.next.WithGroup(name), ring: h.ring, level: h.level, attrs: h.attrs}
}
