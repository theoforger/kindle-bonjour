package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const fakeToken = "test-token"

// fakeHA mirrors testdata/mock_ha.py, plus GET /api/states, /api/ and call logs.
type fakeHA struct {
	*httptest.Server
	mu     sync.Mutex
	states map[string]string
	calls  []string // "POST /api/services/..." entity
	reads  int
	down   bool
	// noServices makes GET /api/services fail, as on an HA that refuses it.
	noServices bool
}

// fakeServices is what the fake HA's GET /api/services lists.
var fakeServices = []map[string]any{
	{"domain": "homeassistant", "services": map[string]any{"toggle": map[string]any{}, "restart": map[string]any{}}},
	{"domain": "light", "services": map[string]any{"turn_on": map[string]any{}, "toggle": map[string]any{"name": "Toggle"}}},
	{"domain": "switch", "services": map[string]any{"toggle": map[string]any{}}},
	{"domain": "input_boolean", "services": map[string]any{"toggle": map[string]any{}}},
	{"domain": "scene", "services": map[string]any{"turn_on": map[string]any{}}},
	{"domain": "script", "services": map[string]any{"turn_on": map[string]any{}, "toggle": map[string]any{}}},
	{"domain": "media_player", "services": map[string]any{"toggle": map[string]any{}, "media_play_pause": map[string]any{}}},
	{"domain": "cover", "services": map[string]any{"toggle": map[string]any{}, "open_cover": map[string]any{}}},
	{"domain": "lock", "services": map[string]any{"lock": map[string]any{}, "unlock": map[string]any{}}},
	{"domain": "shell_command", "services": map[string]any{
		"monitor_off":    map[string]any{},
		"restart_kindle": map[string]any{"name": "Restart the Kindle"},
	}},
}

// fakeToggle is what HA's toggle does to a state.
func fakeToggle(id, state string) string {
	switch entityDomain(id) {
	case "media_player":
		if state == "off" || state == "standby" {
			return "idle"
		}
		return "off"
	case "cover":
		if state == "closed" {
			return "open"
		}
		return "closed"
	}
	if state == "on" {
		return "off"
	}
	return "on"
}

func newFakeHA(t *testing.T) *fakeHA {
	f := &fakeHA{states: map[string]string{
		"light.living_room": "on", "light.kitchen": "off", "switch.porch": "off",
		"switch.fan": "on", "scene.movie_night": "scening", "input_boolean.guest_mode": "off",
		"sensor.outside_temp": "3.1", "media_player.tv": "playing", "cover.garage": "closed",
		"lock.front_door": "locked",
	}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeHA) set(id, state string) {
	f.mu.Lock()
	f.states[id] = state
	f.mu.Unlock()
}

func (f *fakeHA) get(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states[id]
}

func (f *fakeHA) setDown(d bool) {
	f.mu.Lock()
	f.down = d
	f.mu.Unlock()
}

func (f *fakeHA) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeHA) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+fakeToken {
		http.Error(w, "{}", http.StatusUnauthorized)
		return
	}
	state := func(id string) map[string]any {
		return map[string]any{"entity_id": id, "state": f.states[id],
			"attributes": map[string]any{"friendly_name": "HA " + id}}
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/api/":
		json.NewEncoder(w).Encode(map[string]string{"message": "API running."})
	case r.Method == "GET" && r.URL.Path == "/api/services":
		if f.noServices {
			http.Error(w, "{}", http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(fakeServices)
	case r.Method == "GET" && r.URL.Path == "/api/states":
		var all []map[string]any
		for id := range f.states {
			all = append(all, state(id))
		}
		json.NewEncoder(w).Encode(all)
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/states/"):
		f.reads++
		id := strings.TrimPrefix(r.URL.Path, "/api/states/")
		if _, ok := f.states[id]; !ok {
			http.Error(w, "{}", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(state(id))
	case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/api/services/"):
		var body struct {
			EntityID string `json:"entity_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.calls = append(f.calls, r.URL.Path+" "+body.EntityID)
		if name, ok := strings.CutPrefix(r.URL.Path, "/api/services/shell_command/"); ok {
			if name != "monitor_off" && name != "restart_kindle" {
				http.Error(w, `{"message":"Service not found."}`, http.StatusBadRequest)
				return
			}
		}
		if strings.HasSuffix(r.URL.Path, "/homeassistant/toggle") {
			f.states[body.EntityID] = fakeToggle(body.EntityID, f.states[body.EntityID])
		}
		w.Write([]byte("[]"))
	default:
		http.NotFound(w, r)
	}
}
