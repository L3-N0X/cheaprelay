package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	srt "github.com/datarhei/gosrt"
)

//go:embed web
var webFS embed.FS

type API struct {
	settings *Settings
	sup      *Supervisor
	in       *Ingest
	preview  *Preview
	logs     *LogRing
	log      *slog.Logger
}

type statusResponse struct {
	Output  OutputStatus  `json:"output"`
	Input   inputStatus   `json:"input"`
	Preview *PreviewFrame `json:"preview,omitempty"`
	Relay   relayInfo     `json:"relay"`
}

// relayInfo is what the web UI needs to show how to connect the phone.
type relayInfo struct {
	SRTPort         string `json:"srt_port"`
	SRTStreamID     string `json:"srt_stream_id,omitempty"`
	SRTPassphrase   bool   `json:"srt_passphrase"`
	SRTLatencyMs    int64  `json:"srt_latency_ms"`
	StreamKeySet    bool   `json:"stream_key_set"`
	PreviewEnabled  bool   `json:"preview_enabled"`
	InputTimeoutMs  int64  `json:"input_timeout_ms"`
	LiveStableMs    int64  `json:"live_stable_ms"`
	FallbackMaxSecs int64  `json:"fallback_max_s"`
	LogSeq          uint64 `json:"log_seq"`
}

type inputStatus struct {
	Connected      bool      `json:"connected"`
	Active         bool      `json:"active"`
	StreamID       string    `json:"stream_id,omitempty"`
	RemoteAddr     string    `json:"remote_addr,omitempty"`
	ConnectedAt    time.Time `json:"connected_at,omitzero"`
	ActiveSince    time.Time `json:"active_since,omitzero"`
	LastDataMsAgo  *int64    `json:"last_data_ms_ago"`
	VideoCodec     string    `json:"video_codec,omitempty"`
	BitrateKbps    int       `json:"bitrate_kbps"`
	RTTMs          float64   `json:"rtt_ms"`
	PacketsLost    uint64    `json:"packets_lost"`
	PacketsDropped uint64    `json:"packets_dropped"`
	KeyframeCache  int       `json:"keyframe_cache_bytes"`
}

func NewAPI(settings *Settings, sup *Supervisor, in *Ingest, preview *Preview, logs *LogRing, log *slog.Logger) *API {
	return &API{settings: settings, sup: sup, in: in, preview: preview, logs: logs, log: log.With("component", "api")}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	web, _ := fs.Sub(webFS, "web")
	files := http.FileServerFS(web)
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/fonts/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})

	mux.Handle("GET /api/status", a.auth(http.HandlerFunc(a.handleStatus)))
	mux.Handle("GET /api/preview.jpg", a.auth(http.HandlerFunc(a.handlePreview)))
	mux.Handle("GET /api/logs", a.auth(http.HandlerFunc(a.handleLogs)))
	mux.Handle("GET /api/settings", a.auth(http.HandlerFunc(a.handleSettings)))
	mux.Handle("PATCH /api/settings", a.auth(http.HandlerFunc(a.handleUpdateSettings)))
	mux.Handle("POST /api/start", a.action("start", a.sup.Start))
	mux.Handle("POST /api/stop", a.action("stop", a.sup.Stop))
	mux.Handle("POST /api/reconnect", a.action("reconnect", a.sup.Reconnect))
	mux.Handle("POST /api/fallback/force", a.action("force fallback", func(ctx context.Context) error {
		return a.sup.SetForceFallback(ctx, true)
	}))
	mux.Handle("POST /api/fallback/release", a.action("release fallback", func(ctx context.Context) error {
		return a.sup.SetForceFallback(ctx, false)
	}))
	return mux
}

func (a *API) status() statusResponse {
	st := a.in.Snapshot()
	in := inputStatus{
		Connected:     st.Connected,
		Active:        st.Active,
		StreamID:      st.StreamID,
		RemoteAddr:    st.RemoteAddr,
		ConnectedAt:   st.ConnectedAt,
		VideoCodec:    st.VideoCodec,
		BitrateKbps:   st.BitrateKbps,
		KeyframeCache: st.CacheBytes,
	}
	if st.Active {
		in.ActiveSince = st.ActiveSince
	}
	if !st.LastData.IsZero() {
		ms := time.Since(st.LastData).Milliseconds()
		in.LastDataMsAgo = &ms
	}
	if st.conn != nil {
		var s srt.Statistics
		st.conn.Stats(&s)
		in.RTTMs = s.Instantaneous.MsRTT
		in.PacketsLost = s.Accumulated.PktRecvLoss
		in.PacketsDropped = s.Accumulated.PktRecvDrop
	}
	cfg := a.settings.Get()
	_, port, _ := net.SplitHostPort(cfg.SRTListenAddr)
	resp := statusResponse{Output: a.sup.Status(), Input: in, Relay: relayInfo{
		SRTPort:         port,
		SRTStreamID:     cfg.SRTStreamID,
		SRTPassphrase:   cfg.SRTPassphrase != "",
		SRTLatencyMs:    cfg.SRTLatency.Milliseconds(),
		StreamKeySet:    cfg.StreamKey != "",
		PreviewEnabled:  cfg.Preview,
		InputTimeoutMs:  cfg.InputTimeout.Milliseconds(),
		LiveStableMs:    cfg.LiveStableTime.Milliseconds(),
		FallbackMaxSecs: int64(cfg.FallbackMaxDuration.Seconds()),
		LogSeq:          a.logs.Seq(),
	}}
	if cfg.Preview {
		if f := a.preview.Latest(); f.Seq > 0 {
			resp.Preview = &f
		}
	}
	return resp
}

// handleStatus returns the current state. With ?preview=1 the client also
// signals that it shows the preview, which keeps the snapshots coming.
func (a *API) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("preview") == "1" {
		a.preview.Want()
	}
	writeJSON(w, http.StatusOK, a.status())
}

func (a *API) handlePreview(w http.ResponseWriter, r *http.Request) {
	a.preview.Want()
	f := a.preview.Latest()
	if !a.settings.Get().Preview || len(f.JPEG) == 0 {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	etag := `"` + strconv.FormatUint(f.Seq, 10) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Write(f.JPEG)
}

func (a *API) handleLogs(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	writeJSON(w, http.StatusOK, a.logs.After(after))
}

func (a *API) handleSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.settings.View())
}

// handleUpdateSettings takes {"KEY": "value"} to change and {"KEY": null} to
// reset settings to their .env value.
func (a *API) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var changes map[string]*string
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&changes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	if a.settings.Get().APIToken == "" {
		for k := range changes {
			if k == "TWITCH_STREAM_KEY" || k == "TWITCH_RTMP_URL" || k == "SRT_PASSPHRASE" {
				writeJSON(w, http.StatusForbidden, map[string]string{
					"error": k + " can only be changed from the web UI when API_TOKEN is set"})
				return
			}
		}
	}
	a.log.Info("api request", "action", "update settings", "remote", r.RemoteAddr)
	if _, err := a.settings.Update(changes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, a.settings.View())
}

func (a *API) action(name string, fn func(context.Context) error) http.Handler {
	return a.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.log.Info("api request", "action", name, "remote", r.RemoteAddr)
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := fn(ctx); err != nil {
			code := http.StatusServiceUnavailable
			if errors.Is(err, errNoStreamKey) {
				code = http.StatusConflict
			}
			writeJSON(w, code, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, a.status())
	}))
}

// auth accepts "Authorization: Bearer <token>" or "?token=<token>".
func (a *API) auth(next http.Handler) http.Handler {
	token := a.settings.Get().APIToken
	if token == "" {
		return next
	}
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.URL.Query().Get("token")
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			got = strings.TrimPrefix(h, "Bearer ")
		}
		if subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
