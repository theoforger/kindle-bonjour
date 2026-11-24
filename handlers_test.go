package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

type testEnv struct {
	ha    *fakeHA
	dd    *fakeDD
	clk   *fakeClock
	app   *App
	srv   *httptest.Server
	store *ConfigStore
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	e := &testEnv{ha: newFakeHA(t), dd: newFakeDD(t),
		clk: &fakeClock{t: time.Date(2026, 11, 23, 18, 0, 10, 0, time.UTC)}}
	dir := t.TempDir()
	store, err := LoadConfig(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Replace(testConfig(e.ha.URL)); err != nil {
		t.Fatal(err)
	}
	e.store = store
	e.app = NewApp(store, newWeather(e.dd, e.clk), NewIconCache(filepath.Join(dir, "icons")))
	e.app.Now = e.clk.Now
	e.app.afterOp = time.Hour // tests refresh by hand
	e.srv = httptest.NewServer((&Server{App: e.app, PollTimeout: 300 * time.Millisecond}).Handler())
	t.Cleanup(e.srv.Close)
	return e
}

type stateResp struct {
	code    int
	version uint64
	snap    Snapshot
	took    time.Duration
}

func (e *testEnv) poll(t *testing.T, v uint64) stateResp {
	t.Helper()
	start := time.Now()
	resp, err := http.Get(fmt.Sprintf("%s/api/state?v=%d", e.srv.URL, v))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	r := stateResp{code: resp.StatusCode, took: time.Since(start)}
	if resp.StatusCode == 200 {
		var body struct {
			Version  uint64   `json:"version"`
			Snapshot Snapshot `json:"snapshot"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		r.version, r.snap = body.Version, body.Snapshot
		if h := resp.Header.Get("X-Version"); h != strconv.FormatUint(body.Version, 10) {
			t.Fatalf("X-Version %q, body %d", h, body.Version)
		}
	}
	return r
}

func TestLongPoll(t *testing.T) {
	e := newEnv(t)
	v1 := e.app.Refresh(context.Background())

	// Unknown or missing version: answered at once.
	r := e.poll(t, 0)
	if r.code != 200 || r.version != v1 || r.took > 200*time.Millisecond {
		t.Fatalf("v=0: %+v", r)
	}
	if len(r.snap.Switches) != 3 || r.snap.Switches[0].State != "on" || r.snap.Clock != "18:00" {
		t.Fatalf("snapshot %+v", r.snap)
	}

	// Current version and nothing changes: 204 after the timeout.
	r = e.poll(t, v1)
	if r.code != http.StatusNoContent || r.took < 250*time.Millisecond {
		t.Fatalf("idle poll: %+v", r)
	}

	// Current version and something changes mid-wait: woken early.
	go func() {
		time.Sleep(50 * time.Millisecond)
		e.ha.set("switch.porch", "on")
		e.app.Refresh(context.Background())
	}()
	r = e.poll(t, v1)
	if r.code != 200 || r.version != v1+1 || r.took > 250*time.Millisecond {
		t.Fatalf("woken poll: %+v", r)
	}
	if r.snap.Switches[1].State != "on" {
		t.Fatalf("porch not on: %+v", r.snap.Switches[1])
	}

	if resp, _ := http.Get(e.srv.URL + "/api/state?v=abc"); resp.StatusCode != 400 {
		t.Fatalf("bad v: %d", resp.StatusCode)
	}
}

func TestVersionOnlyChangesWithVisibleState(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v := e.app.Refresh(ctx)
	if e.app.Refresh(ctx) != v {
		t.Fatal("version changed with nothing new")
	}
	// Seconds passing within the same minute is invisible.
	e.clk.Add(30 * time.Second)
	if e.app.Refresh(ctx) != v {
		t.Fatal("version changed within the minute")
	}
	// A non-visible config change (weather refresh interval) is invisible.
	e.store.Update(func(c *Config) error { c.ECCC.RefreshSeconds = 900; return nil })
	if e.app.Refresh(ctx) != v {
		t.Fatal("invisible config change bumped the version")
	}
	// The clock minute is visible.
	e.clk.Add(30 * time.Second)
	if v2 := e.app.Refresh(ctx); v2 != v+1 {
		t.Fatalf("minute change: %d -> %d", v, v2)
	}
	v++
	// So are display settings, weather and HA errors.
	e.store.Update(func(c *Config) error { c.Display.Rotation = 90; return nil })
	if v2 := e.app.Refresh(ctx); v2 != v+1 {
		t.Fatal("rotation change not visible")
	}
	v++
	e.app.Weather.Refresh(ctx, e.store.Get().WeatherSource())
	if v2 := e.app.Refresh(ctx); v2 != v+1 {
		t.Fatal("weather arrival not visible")
	}
	v++
	_, v, _ = e.app.Hub.Current()
	e.ha.setDown(true)
	if v2 := e.app.Refresh(ctx); v2 != v+1 {
		t.Fatal("HA outage not visible")
	}
	s, _, _ := e.app.Hub.Current()
	if s.HAError == "" || s.Switches[0].Available() {
		t.Fatalf("outage snapshot %+v", s)
	}
}

func TestStaleWeatherInSnapshot(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.app.Weather.Refresh(ctx, e.store.Get().WeatherSource())
	e.app.Refresh(ctx)
	s, _, _ := e.app.Hub.Current()
	if s.Weather == nil || s.WeatherStale || s.WeatherUpdated != "18:00" {
		t.Fatalf("fresh: %+v", s)
	}
	e.clk.Add(11 * time.Minute)
	e.dd.set(func(f *fakeDD) { f.fail = true })
	e.app.Weather.Refresh(ctx, e.store.Get().WeatherSource())
	e.app.Refresh(ctx)
	s, _, _ = e.app.Hub.Current()
	if s.Weather == nil || s.Weather.Temp != "-2.4" || !s.WeatherStale || s.WeatherUpdated != "18:00" {
		t.Fatalf("stale: %+v", s)
	}
}

func TestLanguageSwitch(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.app.Weather.Refresh(ctx, e.store.Get().WeatherSource())
	v := e.app.Refresh(ctx)
	s, _, _ := e.app.Hub.Current()
	if s.Language != "en" || s.Clock != "18:00" || s.Date != "Mon Nov 23" || s.Weather.Condition != "Mostly Cloudy" {
		t.Fatalf("english %+v", s)
	}

	e.store.Update(func(c *Config) error { c.Language = LangFR; return nil })
	if v2 := e.app.Refresh(ctx); v2 != v+1 {
		t.Fatal("language change not visible")
	}
	s, _, _ = e.app.Hub.Current()
	if s.Language != "fr" || s.Clock != "18 h 00" || s.Date != "lun. 23 nov." {
		t.Fatalf("french %+v", s)
	}
	// The weather cache belongs to the old language: dropped, then refetched in French.
	if s.Weather != nil {
		t.Fatal("english weather kept after switching to french")
	}
	if !e.app.Weather.Refresh(ctx, e.store.Get().WeatherSource()) {
		t.Fatal("no refetch after language change")
	}
	e.app.Refresh(ctx)
	s, _, _ = e.app.Hub.Current()
	if s.Weather == nil || s.Weather.Condition != "Partiellement nuageux" {
		t.Fatalf("french weather %+v", s.Weather)
	}

	e.ha.setDown(true)
	e.app.Refresh(ctx)
	s, _, _ = e.app.Hub.Current()
	if T(s.Language, s.HAError) != "Home Assistant injoignable" {
		t.Fatalf("ha error %q", s.HAError)
	}
}

func post(t *testing.T, u string) (int, string) {
	t.Helper()
	resp, err := http.Post(u, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestToggleEndpoint(t *testing.T) {
	e := newEnv(t)
	v := e.app.Refresh(context.Background())

	// Real HA entities that aren't on the dashboard are refused, and HA is never called.
	for _, id := range []string{"light.kitchen", "", "../api/states"} {
		if code, body := post(t, e.srv.URL+"/api/toggle?id="+id); code != 404 {
			t.Fatalf("%q: %d %s", id, code, body)
		}
	}
	if len(e.ha.callLog()) != 0 {
		t.Fatalf("HA called for unlisted entity: %q", e.ha.callLog())
	}

	if code, body := post(t, e.srv.URL+"/api/toggle?id=switch.porch"); code != 200 {
		t.Fatalf("toggle: %d %s", code, body)
	}
	if e.ha.get("switch.porch") != "on" {
		t.Fatal("porch not toggled")
	}
	// The toggle asks the run loop for a refresh.
	select {
	case <-e.app.trigger:
	default:
		t.Fatal("toggle did not trigger a refresh")
	}
	if v2 := e.app.Refresh(context.Background()); v2 != v+1 {
		t.Fatal("toggle not visible")
	}

	if resp, _ := http.Get(e.srv.URL + "/api/toggle?id=switch.porch"); resp.StatusCode != 405 {
		t.Fatalf("GET toggle: %d", resp.StatusCode)
	}

	e.ha.setDown(true)
	if code, _ := post(t, e.srv.URL+"/api/toggle?id=switch.porch"); code != 502 {
		t.Fatalf("HA down: %d", code)
	}
	e.store.ClearToken()
	if code, _ := post(t, e.srv.URL+"/api/toggle?id=switch.porch"); code != 409 {
		t.Fatalf("setup mode: %d", code)
	}
}

func TestSetupModeSnapshot(t *testing.T) {
	e := newEnv(t)
	e.store.ClearToken()
	e.app.Refresh(context.Background())
	s, _, _ := e.app.Hub.Current()
	if !s.Setup || len(s.Switches) != 0 {
		t.Fatalf("%+v", s)
	}
	if e.ha.reads != 0 {
		t.Fatal("HA read in setup mode")
	}
}

func TestHealthz(t *testing.T) {
	e := newEnv(t)
	e.app.Refresh(context.Background())
	resp, err := http.Get(e.srv.URL + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatal(err, resp.StatusCode)
	}
	defer resp.Body.Close()
	var h struct {
		Status  string `json:"status"`
		Version uint64 `json:"version"`
		Setup   bool   `json:"setup"`
		HA      struct {
			OKAt *string `json:"ok_at"`
		} `json:"home_assistant"`
	}
	json.NewDecoder(resp.Body).Decode(&h)
	if h.Status != "ok" || h.Version == 0 || h.Setup || h.HA.OKAt == nil {
		t.Fatalf("%+v", h)
	}
}

func TestHubVersionsSurviveRestart(t *testing.T) {
	// A Kindle holding version 1 from an old server must not match a new one.
	h := NewHub(time.Now())
	v := h.Publish(Snapshot{Clock: "12:00"})
	if v <= 1 {
		t.Fatalf("version %d", v)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, got, ok := h.Wait(ctx, 1); !ok || got != v {
		t.Fatal("old version should return at once")
	}
}

func TestWaitBeforeFirstPublish(t *testing.T) {
	h := NewHub(time.Now())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, _, ok := h.Wait(ctx, 0); ok {
		t.Fatal("nothing published yet, should time out")
	}
}

// With the real run loop, a language save shows French weather well before
// the 10-second weather tick.
func TestRunRefetchesWeatherAfterSave(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.app.Run(ctx)

	waitFor := func(what string, ok func(Snapshot) bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		var v uint64
		for time.Now().Before(deadline) {
			wctx, c := context.WithDeadline(ctx, deadline)
			s, nv, got := e.app.Hub.Wait(wctx, v)
			c()
			if got && ok(s) {
				return
			}
			v = nv
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	waitFor("english weather", func(s Snapshot) bool { return s.Weather != nil && s.Weather.Condition == "Mostly Cloudy" })
	e.store.Update(func(c *Config) error { c.Language = LangFR; return nil })
	waitFor("french weather", func(s Snapshot) bool {
		return s.Language == "fr" && s.Weather != nil && s.Weather.Condition == "Partiellement nuageux"
	})
}
