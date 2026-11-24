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
}

func newFakeHA(t *testing.T) *fakeHA {
	f := &fakeHA{states: map[string]string{
		"light.living_room": "on", "light.kitchen": "off", "switch.porch": "off",
		"switch.fan": "on", "scene.movie_night": "scening", "input_boolean.guest_mode": "off",
		"sensor.outside_temp": "3.1",
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
		if strings.HasSuffix(r.URL.Path, "/homeassistant/toggle") {
			if f.states[body.EntityID] == "on" {
				f.states[body.EntityID] = "off"
			} else {
				f.states[body.EntityID] = "on"
			}
		}
		w.Write([]byte("[]"))
	default:
		http.NotFound(w, r)
	}
}
