package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestReadStates(t *testing.T) {
	f := newFakeHA(t)
	c := NewHAClient(HAConfig{URL: f.URL + "/", Token: fakeToken})
	got, err := c.ReadStates(context.Background(), []Entity{
		{ID: "light.living_room", Name: "Living Room"},
		{ID: "switch.porch"},
		{ID: "scene.movie_night"},
		{ID: "light.missing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []SwitchState{
		{ID: "light.living_room", Name: "Living Room", State: "on"},
		{ID: "switch.porch", Name: "HA switch.porch", State: "off"},
		{ID: "scene.movie_night", Name: "HA scene.movie_night", State: "scening", Action: true},
		{ID: "light.missing", Name: "light.missing", State: "unavailable"},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: got %+v want %+v", i, got[i], want[i])
		}
	}
	if !got[0].On() || got[1].On() || got[2].On() || got[3].Available() {
		t.Error("On/Available wrong")
	}
}

func TestReadStatesErrors(t *testing.T) {
	f := newFakeHA(t)
	ents := []Entity{{ID: "light.kitchen"}}
	_, err := NewHAClient(HAConfig{URL: f.URL, Token: "wrong"}).ReadStates(context.Background(), ents)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad token: %v", err)
	}
	f.setDown(true)
	got, err := NewHAClient(HAConfig{URL: f.URL, Token: fakeToken}).ReadStates(context.Background(), ents)
	if err == nil || got[0].State != "unavailable" {
		t.Fatalf("down: %v %+v", err, got)
	}
}

func TestToggleServices(t *testing.T) {
	f := newFakeHA(t)
	c := NewHAClient(HAConfig{URL: f.URL, Token: fakeToken})
	for _, id := range []string{"light.kitchen", "scene.movie_night"} {
		if err := c.Toggle(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	calls := f.callLog()
	if len(calls) != 2 || calls[0] != "/api/services/homeassistant/toggle light.kitchen" ||
		calls[1] != "/api/services/scene/turn_on scene.movie_night" {
		t.Fatalf("calls: %q", calls)
	}
	if f.get("light.kitchen") != "on" {
		t.Fatal("kitchen not toggled")
	}
}

func TestEntitiesAndPing(t *testing.T) {
	f := newFakeHA(t)
	c := NewHAClient(HAConfig{URL: f.URL, Token: fakeToken})
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	es, err := c.Entities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Domains with a toggle service, actions and shell commands; not the
	// sensor or the lock (no toggle).
	var ids []string
	byID := map[string]EntityInfo{}
	for _, e := range es {
		ids = append(ids, e.ID)
		byID[e.ID] = e
	}
	want := []string{"cover.garage", "input_boolean.guest_mode", "light.kitchen", "light.living_room",
		"media_player.tv", "scene.movie_night", "shell_command.monitor_off", "shell_command.restart_kindle",
		"switch.fan", "switch.porch"}
	if strings.Join(ids, " ") != strings.Join(want, " ") {
		t.Fatalf("entities:\n got %v\nwant %v", ids, want)
	}
	if e := byID["shell_command.monitor_off"]; e.Name != "Monitor off" || e.Domain != "shell_command" {
		t.Errorf("shell command without a name: %+v", e)
	}
	if e := byID["shell_command.restart_kindle"]; e.Name != "Restart the Kindle" {
		t.Errorf("shell command name not used: %+v", e)
	}

	// Without the service list: a fixed list of domains, no shell commands.
	f.noServices = true
	es, err = c.Entities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ids = nil
	for _, e := range es {
		ids = append(ids, e.ID)
	}
	if got := strings.Join(ids, " "); strings.Contains(got, "shell_command") || !strings.Contains(got, "media_player.tv") ||
		strings.Contains(got, "lock.") {
		t.Errorf("fallback entities: %v", ids)
	}
}

func TestShellCommands(t *testing.T) {
	f := newFakeHA(t)
	c := NewHAClient(HAConfig{URL: f.URL, Token: fakeToken})
	ents := []Entity{{ID: "light.kitchen"}, {ID: "shell_command.monitor_off"}, {ID: "shell_command.restart_kindle", Name: "Kindle"}}
	got, err := c.ReadStates(context.Background(), ents)
	if err != nil {
		t.Fatal(err)
	}
	if f.reads != 1 {
		t.Errorf("%d state reads, want 1 (shell commands have no state)", f.reads)
	}
	want := []SwitchState{
		{ID: "light.kitchen", Name: "HA light.kitchen", State: "off"},
		{ID: "shell_command.monitor_off", Name: "Monitor off", State: shellReady, Action: true},
		{ID: "shell_command.restart_kindle", Name: "Kindle", State: shellReady, Action: true},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: got %+v want %+v", i, got[i], want[i])
		}
	}
	if got[1].StateKey() != "tile_run" {
		t.Errorf("shell command shows %s", got[1].StateKey())
	}

	// Running one calls the service, with no entity_id.
	if err := c.Toggle(context.Background(), "shell_command.monitor_off"); err != nil {
		t.Fatal(err)
	}
	if calls := f.callLog(); len(calls) != 1 || calls[0] != "/api/services/shell_command/monitor_off " {
		t.Fatalf("calls: %q", calls)
	}
	if err := c.Toggle(context.Background(), "shell_command.gone"); err == nil {
		t.Error("an unknown shell command succeeded")
	}

	// HA down: shell command tiles are unavailable too.
	f.setDown(true)
	got, err = c.ReadStates(context.Background(), ents)
	if err == nil || got[1].Available() {
		t.Errorf("down: %v %+v", err, got[1])
	}
	// Only shell commands: HA's status still comes through (from a ping).
	only := ents[1:]
	if _, err := c.ReadStates(context.Background(), only); err == nil {
		t.Error("only shell commands, HA down: no error")
	}
	f.setDown(false)
	got, err = c.ReadStates(context.Background(), only)
	if err != nil || !got[0].Available() {
		t.Errorf("only shell commands, HA up: %v %+v", err, got)
	}
}

func TestDomainStates(t *testing.T) {
	for _, tc := range []struct {
		id, state string
		on        bool
		key       string
	}{
		{"light.a", "on", true, "tile_on"},
		{"light.a", "off", false, "tile_off"},
		{"media_player.tv", "playing", true, "tile_playing"},
		{"media_player.tv", "paused", true, "tile_paused"},
		{"media_player.tv", "idle", true, "tile_on"},
		{"media_player.tv", "on", true, "tile_on"},
		{"media_player.tv", "standby", false, "tile_off"},
		{"media_player.tv", "off", false, "tile_off"},
		{"cover.garage", "open", true, "tile_open"},
		{"cover.garage", "opening", true, "tile_open"},
		{"cover.garage", "closed", false, "tile_closed"},
		{"valve.main", "closed", false, "tile_closed"},
		{"climate.living", "heat", true, "tile_on"},
		{"climate.living", "off", false, "tile_off"},
		{"media_player.tv", "unavailable", false, "tile_na"},
		{"scene.x", "scening", false, "tile_run"},
	} {
		sw := SwitchState{ID: tc.id, State: tc.state, Action: IsAction(tc.id)}
		if sw.On() != tc.on || sw.StateKey() != tc.key {
			t.Errorf("%s %s: on=%v key=%s, want %v %s", tc.id, tc.state, sw.On(), sw.StateKey(), tc.on, tc.key)
		}
	}
	for _, k := range []string{"tile_open", "tile_closed", "tile_playing", "tile_paused"} {
		if T(LangFR, k) == k || T(LangEN, k) == k {
			t.Errorf("%s has no translation", k)
		}
	}
}
