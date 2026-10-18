package main

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	srt "github.com/datarhei/gosrt"
)

//go:embed ui.html
var uiHTML []byte

type API struct {
	cfg Config
	sup *Supervisor
	in  *Ingest
	log *slog.Logger
}

type statusResponse struct {
	Output OutputStatus `json:"output"`
	Input  inputStatus  `json:"input"`
}

type inputStatus struct {
	Connected      bool      `json:"connected"`
	Active         bool      `json:"active"`
	StreamID       string    `json:"stream_id,omitempty"`
	RemoteAddr     string    `json:"remote_addr,omitempty"`
	ConnectedAt    time.Time `json:"connected_at,omitzero"`
	LastDataMsAgo  *int64    `json:"last_data_ms_ago"`
	VideoCodec     string    `json:"video_codec,omitempty"`
	BitrateKbps    int       `json:"bitrate_kbps"`
	RTTMs          float64   `json:"rtt_ms"`
	PacketsLost    uint64    `json:"packets_lost"`
	PacketsDropped uint64    `json:"packets_dropped"`
	KeyframeCache  int       `json:"keyframe_cache_bytes"`
}

func NewAPI(cfg Config, sup *Supervisor, in *Ingest, log *slog.Logger) *API {
	return &API{cfg: cfg, sup: sup, in: in, log: log.With("component", "api")}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(uiHTML)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})

	mux.Handle("GET /api/status", a.auth(http.HandlerFunc(a.handleStatus)))
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
	return statusResponse{Output: a.sup.Status(), Input: in}
}

func (a *API) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.status())
}

func (a *API) action(name string, fn func(context.Context) error) http.Handler {
	return a.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.log.Info("api request", "action", name, "remote", r.RemoteAddr)
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := fn(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, a.status())
	}))
}

// auth accepts "Authorization: Bearer <token>" or "?token=<token>".
func (a *API) auth(next http.Handler) http.Handler {
	if a.cfg.APIToken == "" {
		return next
	}
	want := []byte(a.cfg.APIToken)
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
