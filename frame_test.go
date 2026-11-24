package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"strconv"
	"testing"
	"time"
)

type frameResp struct {
	code    int
	version uint64
	refresh string
	lang    string
	img     image.Image
}

func (e *testEnv) frame(t *testing.T, v uint64, extra string) frameResp {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("%s/frame.png?v=%d%s", e.srv.URL, v, extra))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	r := frameResp{code: resp.StatusCode, refresh: resp.Header.Get("X-Refresh"), lang: resp.Header.Get("X-Language")}
	if resp.StatusCode == 200 {
		if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
			t.Fatalf("content type %q", ct)
		}
		r.version, _ = strconv.ParseUint(resp.Header.Get("X-Version"), 10, 64)
		if r.img, err = png.Decode(resp.Body); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

type tapResp struct {
	OK    bool   `json:"ok"`
	Hit   bool   `json:"hit"`
	ID    string `json:"id"`
	Error string `json:"error"`
	code  int
}

func (e *testEnv) tap(t *testing.T, x, y int, v uint64) tapResp {
	t.Helper()
	resp, err := http.Post(fmt.Sprintf("%s/api/tap?x=%d&y=%d&v=%d", e.srv.URL, x, y, v), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var r tapResp
	json.NewDecoder(resp.Body).Decode(&r)
	r.code = resp.StatusCode
	return r
}

// tileCenter gives the panel point at the centre of a tile in a snapshot.
func tileCenter(t *testing.T, s Snapshot, id string) (int, int) {
	t.Helper()
	l := ComputeLayout(s)
	for _, tile := range l.Tiles {
		if tile.ID == id {
			c := tile.Rect.Min.Add(tile.Rect.Max).Div(2)
			return toPanel(l.Rotation, c.X, c.Y)
		}
	}
	t.Fatalf("no tile %s", id)
	return 0, 0
}

// eventually refreshes until cond holds, or fails after 2 s.
func (e *testEnv) eventually(t *testing.T, what string, cond func(Snapshot) bool) Snapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		e.app.Refresh(context.Background())
		s, _, _ := e.app.Hub.Current()
		if cond(s) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; snapshot %+v", what, s)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestFrameLongPoll(t *testing.T) {
	e := newEnv(t)
	v1 := e.app.Refresh(context.Background())

	r := e.frame(t, 0, "")
	if r.code != 200 || r.version != v1 || r.refresh != "full" || r.lang != "en" {
		t.Fatalf("first frame: %+v", r)
	}
	if r.img.Bounds() != image.Rect(0, 0, PanelW, PanelH) {
		t.Fatalf("frame size %v", r.img.Bounds())
	}
	if _, ok := r.img.(*image.Gray); !ok {
		t.Fatalf("frame is %T, want 8-bit gray", r.img)
	}
	// Nothing new: 204.
	if r := e.frame(t, v1, ""); r.code != http.StatusNoContent {
		t.Fatalf("idle: %+v", r)
	}
	// A change: new frame, no flash (the interval hasn't passed).
	e.ha.set("switch.porch", "on")
	v2 := e.app.Refresh(context.Background())
	if r := e.frame(t, v1, ""); r.code != 200 || r.version != v2 || r.refresh != "" {
		t.Fatalf("changed: %+v", r)
	}
	// After flash_minutes, the next frame flashes.
	e.clk.Add(31 * time.Minute)
	v3 := e.app.Refresh(context.Background()) // clock minute changed
	if r := e.frame(t, v2, ""); r.code != 200 || r.version != v3 || r.refresh != "full" {
		t.Fatalf("flash due: %+v", r)
	}
	if resp, _ := http.Get(e.srv.URL + "/frame.png?v=x"); resp.StatusCode != 400 {
		t.Fatalf("bad v: %d", resp.StatusCode)
	}
}

func TestDebugPollsDontCountAsKindle(t *testing.T) {
	e := newEnv(t)
	e.app.Refresh(context.Background())
	health := func() map[string]any {
		var h struct {
			Kindle map[string]any `json:"kindle"`
		}
		resp, err := http.Get(e.srv.URL + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		json.NewDecoder(resp.Body).Decode(&h)
		return h.Kindle
	}
	if r := e.frame(t, 0, "&debug=1"); r.code != 200 || r.refresh != "" {
		t.Fatalf("debug frame: %+v", r)
	}
	if k := health(); k["last_seen"] != nil {
		t.Fatalf("debug poll counted as the Kindle: %v", k)
	}
	// The Kindle's first frame still flashes after a debug poll.
	if r := e.frame(t, 0, ""); r.refresh != "full" {
		t.Fatalf("kindle frame: %+v", r)
	}
	k := health()
	if k["last_seen"] != "2026-11-23T18:00:10Z" || k["addr"] != "127.0.0.1" {
		t.Fatalf("kindle status %v", k)
	}
}

func TestTapTogglesWithPendingBorder(t *testing.T) {
	e := newEnv(t)
	v1 := e.app.Refresh(context.Background())
	e.frame(t, 0, "")
	snap, _, _ := e.app.Hub.Current()

	x, y := tileCenter(t, snap, "light.living_room")
	r := e.tap(t, x, y, v1)
	if r.code != http.StatusAccepted || !r.Hit || r.ID != "light.living_room" {
		t.Fatalf("tap: %+v", r)
	}
	// The pending border is published before the tap call returns.
	s, v2, _ := e.app.Hub.Current()
	if v2 == v1 || len(s.Pending) != 1 || s.Pending[0] != "light.living_room" {
		t.Fatalf("pending not shown: v %d->%d %+v", v1, v2, s.Pending)
	}
	if fr := e.frame(t, v1, ""); fr.code != 200 || fr.version != v2 {
		t.Fatalf("pending frame: %+v", fr)
	}
	// HA toggles; the next refresh shows the new state and drops the border.
	s = e.eventually(t, "toggle confirmed", func(s Snapshot) bool { return len(s.Pending) == 0 })
	if e.ha.get("light.living_room") != "off" || s.Switches[0].State != "off" {
		t.Fatalf("not toggled: HA %s, snapshot %+v", e.ha.get("light.living_room"), s.Switches[0])
	}

	// An action tile (scene) clears as soon as HA accepted the call.
	_, v, _ := e.app.Hub.Current()
	e.frame(t, 0, "")
	x, y = tileCenter(t, s, "scene.movie_night")
	if r := e.tap(t, x, y, v); r.ID != "scene.movie_night" {
		t.Fatalf("scene tap: %+v", r)
	}
	e.eventually(t, "scene confirmed", func(s Snapshot) bool { return len(s.Pending) == 0 })
	want := "/api/services/scene/turn_on scene.movie_night"
	if calls := e.ha.callLog(); calls[len(calls)-1] != want {
		t.Fatalf("calls %q", calls)
	}
}

func TestTapErrors(t *testing.T) {
	e := newEnv(t)
	v := e.app.Refresh(context.Background())
	e.frame(t, 0, "")
	snap, _, _ := e.app.Hub.Current()
	l := ComputeLayout(snap)

	if r := e.tap(t, 10, 10, v+1000); r.code != http.StatusConflict {
		t.Fatalf("unknown version: %+v", r)
	}
	wx, wy := toPanel(l.Rotation, l.Weather.Min.X+20, l.Weather.Min.Y+20)
	if r := e.tap(t, wx, wy, v); r.code != 200 || r.Hit {
		t.Fatalf("weather tap: %+v", r)
	}
	resp, _ := http.Post(e.srv.URL+"/api/tap?x=1&y=2", "", nil)
	if resp.StatusCode != 400 {
		t.Fatalf("missing v: %d", resp.StatusCode)
	}

	// HA rejects the call: the border goes away and nothing changes.
	e.ha.setDown(true)
	x, y := tileCenter(t, snap, "switch.porch")
	if r := e.tap(t, x, y, v); r.code != http.StatusAccepted {
		t.Fatalf("tap: %+v", r)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		s, _, _ := e.app.Hub.Current()
		if len(s.Pending) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pending border stuck after a failed toggle")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A tile removed from the config since the frame was drawn.
	e.ha.setDown(false)
	e.store.Update(func(c *Config) error { c.Entities = c.Entities[:1]; return nil })
	if r := e.tap(t, x, y, v); r.code != http.StatusConflict || r.Error != "unknown entity" {
		t.Fatalf("removed tile: %+v", r)
	}
}

// A tap is matched against the frame the Kindle was showing, even if the
// rotation changed since.
func TestTapUsesTheFramesOwnLayout(t *testing.T) {
	e := newEnv(t)
	v0 := e.app.Refresh(context.Background())
	e.frame(t, 0, "")
	s0, _, _ := e.app.Hub.Current()

	e.store.Update(func(c *Config) error { c.Display.Rotation = 90; return nil })
	v90 := e.app.Refresh(context.Background())
	e.frame(t, v0, "")
	s90, _, _ := e.app.Hub.Current()

	// Pick the porch tile: at these two rotations its panel centres differ.
	x0, y0 := tileCenter(t, s0, "switch.porch")
	x9, y9 := tileCenter(t, s90, "switch.porch")
	if x0 == x9 && y0 == y9 {
		t.Fatal("test needs different positions")
	}
	if r := e.tap(t, x0, y0, v0); r.ID != "switch.porch" {
		t.Fatalf("old frame: %+v", r)
	}
	e.eventually(t, "first tap", func(s Snapshot) bool { return len(s.Pending) == 0 })
	if r := e.tap(t, x9, y9, v90); r.ID != "switch.porch" {
		t.Fatalf("new frame: %+v", r)
	}
}

func TestPendingExpires(t *testing.T) {
	e := newEnv(t)
	e.app.Refresh(context.Background())
	// HA accepted the call but the state never changed (e.g. the device
	// ignored it): the border goes after pendingAfterDone.
	e.app.mu.Lock()
	e.app.pending["switch.porch"] = &pendingTap{since: e.clk.Now(), prev: "off", doneAt: e.clk.Now()}
	e.app.mu.Unlock()
	if s, _, _ := publishNow(e); len(s.Pending) != 1 {
		t.Fatalf("pending dropped too early: %+v", s.Pending)
	}
	e.clk.Add(pendingAfterDone + time.Second)
	if s, _, _ := publishNow(e); len(s.Pending) != 0 {
		t.Fatalf("pending not dropped: %+v", s.Pending)
	}
	// A call that never finishes still clears after pendingMax.
	e.app.mu.Lock()
	e.app.pending["switch.porch"] = &pendingTap{since: e.clk.Now(), prev: "off"}
	e.app.mu.Unlock()
	e.clk.Add(pendingMax + time.Second)
	if s, _, _ := publishNow(e); len(s.Pending) != 0 {
		t.Fatalf("in-flight pending not dropped: %+v", s.Pending)
	}
}

func publishNow(e *testEnv) (Snapshot, uint64, bool) {
	e.app.Refresh(context.Background())
	return e.app.Hub.Current()
}

func TestDebugPage(t *testing.T) {
	e := newEnv(t)
	resp, err := http.Get(e.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("debug page: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp, _ := http.Get(e.srv.URL + "/nope"); resp.StatusCode != 404 {
		t.Fatalf("unknown path: %d", resp.StatusCode)
	}
}
