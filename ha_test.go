package main

import (
	"context"
	"errors"
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
	if len(es) != 6 { // the sensor is filtered out
		t.Fatalf("got %d entities: %+v", len(es), es)
	}
	if es[0].ID != "input_boolean.guest_mode" || es[0].Domain != "input_boolean" {
		t.Fatalf("not sorted: %+v", es[0])
	}
}
