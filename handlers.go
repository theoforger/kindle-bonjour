package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Server is the HTTP layer over App.
type Server struct {
	App         *App
	PollTimeout time.Duration // long-poll limit, about 25 s
	Renderer    *Renderer     // nil: one using App.Icons

	frames *frameCache

	mu        sync.Mutex
	lastFlash time.Time // last frame sent with X-Refresh: full
	seenAt    time.Time // last /frame.png request from the Kindle
	seenAddr  string
}

func (s *Server) Handler() http.Handler {
	if s.Renderer == nil {
		s.Renderer = NewRenderer(NewIconImages(s.App.Icons).Get)
	}
	s.frames = &frameCache{r: s.Renderer}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.debugPage)
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /frame.png", s.framePNG)
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("POST /api/tap", s.tap)
	mux.HandleFunc("POST /api/toggle", s.toggle)
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	_, version, _ := s.App.Hub.Current()
	haErr, haOK := s.App.HAStatus()
	ws := s.App.Weather.Status(s.App.Config.Get().WeatherSource())
	out := map[string]any{
		"status":  "ok",
		"version": version,
		"setup":   s.App.Config.Get().SetupMode(),
		"weather": map[string]any{"stale": ws.Stale, "fetched_at": nullTime(ws.FetchedAt), "error": ws.Err},
		"home_assistant": map[string]any{
			"ok_at": nullTime(haOK),
			"error": errString(haErr),
		},
		"kindle": s.kindleStatus(),
	}
	writeJSON(w, http.StatusOK, out)
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// parseVersion reads ?v=. Missing means 0, which never matches a real version.
func parseVersion(r *http.Request) (uint64, bool) {
	v := r.URL.Query().Get("v")
	if v == "" {
		return 0, true
	}
	n, err := strconv.ParseUint(v, 10, 64)
	return n, err == nil
}

// state long-polls for a snapshot newer than ?v=. It answers 200 with the
// snapshot as soon as the version differs, or 204 after PollTimeout.
// /frame.png (Stage 2) uses the same wait.
func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	since, ok := parseVersion(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad v"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.PollTimeout)
	defer cancel()
	snap, version, ok := s.App.Hub.Wait(ctx, since)
	if !ok {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("X-Version", strconv.FormatUint(version, 10))
	writeJSON(w, http.StatusOK, map[string]any{"version": version, "snapshot": snap})
}

func (s *Server) toggle(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	err := s.App.Toggle(ctx, id)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case errors.Is(err, ErrUnknownEntity):
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "unknown entity"})
	case errors.Is(err, ErrSetupMode):
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
	default:
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
	}
}

// kindleStatus reports when the Kindle last polled for a frame.
func (s *Server) kindleStatus() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]any{"last_seen": nullTime(s.seenAt), "addr": s.seenAddr}
	if !s.seenAt.IsZero() {
		out["age_seconds"] = int(s.App.Now().Sub(s.seenAt).Seconds())
	}
	return out
}

// framePNG long-polls like /api/state but answers with the rendered
// frame. Headers: X-Version, and X-Refresh: full when a flashing refresh
// is due (first frame after the agent starts, then every
// display.flash_minutes). X-Language tells the agent which language its
// own messages (offline marker) should use.
//
// ?debug=1 (the debug page) neither counts as the Kindle being seen nor
// uses up the flash.
func (s *Server) framePNG(w http.ResponseWriter, r *http.Request) {
	since, ok := parseVersion(r)
	if !ok {
		http.Error(w, "bad v", http.StatusBadRequest)
		return
	}
	debug := r.URL.Query().Get("debug") != ""
	if !debug {
		s.seen(r)
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.PollTimeout)
	defer cancel()
	snap, version, ok := s.App.Hub.Wait(ctx, since)
	if !ok {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	f, err := s.frames.get(version, snap, "http://"+r.Host)
	if err != nil {
		log.Printf("render: %v", err)
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Version", strconv.FormatUint(version, 10))
	h.Set("X-Language", snap.Language)
	if !debug && s.flashDue(since == 0, snap.Display.FlashMinutes) {
		h.Set("X-Refresh", "full")
	}
	h.Set("Content-Length", strconv.Itoa(len(f.png)))
	w.Write(f.png)
}

func (s *Server) seen(r *http.Request) {
	addr, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		addr = r.RemoteAddr
	}
	s.mu.Lock()
	s.seenAt, s.seenAddr = s.App.Now(), addr
	s.mu.Unlock()
}

// flashDue decides whether this frame gets a full (flashing) refresh, and
// if so restarts the interval.
func (s *Server) flashDue(start bool, minutes int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.App.Now()
	due := start || s.lastFlash.IsZero() ||
		minutes > 0 && now.Sub(s.lastFlash) >= time.Duration(minutes)*time.Minute
	if due {
		s.lastFlash = now
	}
	return due
}

// tap handles POST /api/tap?x=&y=&v=: a tap at panel coordinates on the
// frame of version v. It answers at once; the toggle happens in the
// background and the frame shows the tile as pending until HA confirms.
func (s *Server) tap(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	x, errX := strconv.Atoi(q.Get("x"))
	y, errY := strconv.Atoi(q.Get("y"))
	v, errV := strconv.ParseUint(q.Get("v"), 10, 64)
	if errX != nil || errY != nil || errV != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "need x, y and v"})
		return
	}
	l, ok := s.frames.layout(v)
	if !ok {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "unknown frame version"})
		return
	}
	id, hit := l.HitTest(x, y)
	if !hit {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "hit": false})
		return
	}
	switch err := s.App.Tap(id); {
	case err == nil:
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "hit": true, "id": id})
	case errors.Is(err, ErrUnknownEntity):
		// The tile was removed from the config since that frame.
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "unknown entity"})
	default:
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
	}
}

//go:embed web/debug.html
var debugHTML []byte

func (s *Server) debugPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(debugHTML)
}
