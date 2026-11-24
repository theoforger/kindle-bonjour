package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// Server is the HTTP layer over App.
type Server struct {
	App         *App
	PollTimeout time.Duration // long-poll limit, about 25 s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /api/state", s.state)
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
