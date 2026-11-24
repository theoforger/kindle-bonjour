package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// adminDo sends a JSON request to the admin API and decodes the reply.
func adminDo(t *testing.T, method, u string, body any, hdr map[string]string) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	if len(raw) > 0 && strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, u, err, raw)
		}
	}
	out["_raw"] = string(raw)
	return resp.StatusCode, out
}

func (e *testEnv) getAdminConfig(t *testing.T) map[string]any {
	t.Helper()
	code, c := adminDo(t, "GET", e.srv.URL+"/api/admin/config", nil, nil)
	if code != 200 {
		t.Fatalf("GET config: %d %v", code, c)
	}
	delete(c, "_raw")
	return c
}

func TestAdminConfigRoundTrip(t *testing.T) {
	e := newEnv(t)
	c := e.getAdminConfig(t)
	if strings.Contains(mustJSON(t, c), fakeToken) {
		t.Fatal("GET config leaks the token")
	}
	ha := c["home_assistant"].(map[string]any)
	if ha["token_set"] != true || c["setup"] != false || c["admin_password"] != false {
		t.Fatalf("flags: %v", c)
	}

	// Change everything the page can change, sending back what GET gave
	// (token_set included) with an empty token.
	c["language"] = "fr"
	c["entities"] = []map[string]string{{"id": "switch.porch", "name": "Porch"}, {"id": "light.kitchen"}}
	c["display"].(map[string]any)["rotation"] = 90
	c["display"].(map[string]any)["columns"] = 3
	c["eccc"].(map[string]any)["refresh_seconds"] = 900
	code, out := adminDo(t, "PUT", e.srv.URL+"/api/admin/config", c, nil)
	if code != 200 {
		t.Fatalf("PUT: %d %v", code, out)
	}
	got := e.store.Get()
	if got.Language != LangFR || got.Display.Rotation != 90 || got.Display.Columns != 3 ||
		got.ECCC.RefreshSeconds != 900 || len(got.Entities) != 2 || got.Entities[0].Name != "Porch" {
		t.Fatalf("saved: %+v", got)
	}
	if got.HA.Token != fakeToken {
		t.Fatal("empty token should keep the saved one")
	}
	if out["version"].(float64) != c["version"].(float64)+1 {
		t.Fatalf("version %v after %v", out["version"], c["version"])
	}

	// The next frame follows at once.
	snap := e.eventually(t, "frame follows config", func(s Snapshot) bool {
		return s.Language == LangFR && s.Display.Rotation == 90 && len(s.Switches) == 2
	})
	if snap.Switches[0].ID != "switch.porch" {
		t.Fatalf("order: %+v", snap.Switches)
	}

	// A new token replaces the old one; clear_token removes it.
	c = e.getAdminConfig(t)
	c["home_assistant"] = map[string]any{"url": e.ha.URL, "token": "  new-token "}
	if code, out := adminDo(t, "PUT", e.srv.URL+"/api/admin/config", c, nil); code != 200 {
		t.Fatalf("PUT token: %d %v", code, out)
	}
	if e.store.Get().HA.Token != "new-token" {
		t.Fatalf("token %q", e.store.Get().HA.Token)
	}
	c = e.getAdminConfig(t)
	c["home_assistant"] = map[string]any{"url": e.ha.URL, "clear_token": true}
	if code, out := adminDo(t, "PUT", e.srv.URL+"/api/admin/config", c, nil); code != 200 {
		t.Fatalf("PUT clear: %d %v", code, out)
	}
	c = e.getAdminConfig(t)
	if e.store.Get().HA.Token != "" || c["setup"] != true {
		t.Fatalf("token not cleared: %v", c)
	}
}

func mustJSON(t *testing.T, v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAdminConfigErrors(t *testing.T) {
	e := newEnv(t)
	base := e.getAdminConfig(t)
	put := func(mod func(c map[string]any)) (int, map[string]any) {
		var c map[string]any
		json.Unmarshal([]byte(mustJSON(t, base)), &c)
		mod(c)
		return adminDo(t, "PUT", e.srv.URL+"/api/admin/config", c, nil)
	}
	cases := []struct {
		key, arg string
		mod      func(c map[string]any)
	}{
		{"invalid_url", "", func(c map[string]any) { c["home_assistant"] = map[string]any{"url": "ha.lan:8123"} }},
		{"invalid_entity", "Light.X", func(c map[string]any) { c["entities"] = []map[string]string{{"id": "Light.X"}} }},
		{"duplicate_entity", "switch.porch", func(c map[string]any) {
			c["entities"] = []map[string]string{{"id": "switch.porch"}, {"id": "switch.porch"}}
		}},
		{"invalid_language", "", func(c map[string]any) { c["language"] = "de" }},
		{"invalid_split", "", func(c map[string]any) { c["display"].(map[string]any)["split"] = 0.95 }},
		{"invalid_columns", "", func(c map[string]any) { c["display"].(map[string]any)["columns"] = 9 }},
		{"invalid_rotation", "", func(c map[string]any) { c["display"].(map[string]any)["rotation"] = 45 }},
		{"invalid_flash", "", func(c map[string]any) { c["display"].(map[string]any)["flash_minutes"] = -1 }},
		{"invalid_scale", "weather", func(c map[string]any) {
			c["display"].(map[string]any)["scale"] = map[string]any{"header": 1, "switches": 1, "weather": 3, "forecast": 1}
		}},
		{"invalid_refresh", "", func(c map[string]any) { c["eccc"].(map[string]any)["refresh_seconds"] = 5 }},
	}
	for _, tc := range cases {
		code, out := put(tc.mod)
		if code != 400 || out["error"] != tc.key || (tc.arg != "" && out["arg"] != tc.arg) {
			t.Errorf("%s: %d %v", tc.key, code, out)
		}
	}
	if v := e.store.Version(); uint64(base["version"].(float64)) != v {
		t.Fatalf("a rejected save changed the version: %d", v)
	}

	// Saving on top of a stale version is a conflict.
	if code, out := put(func(c map[string]any) { c["language"] = "fr" }); code != 200 {
		t.Fatalf("first save: %d %v", code, out)
	}
	code, out := put(func(c map[string]any) { c["language"] = "en" })
	if code != 409 || out["error"] != "config_conflict" || e.store.Get().Language != LangFR {
		t.Fatalf("stale save: %d %v", code, out)
	}

	if code, out := adminDo(t, "PUT", e.srv.URL+"/api/admin/config", "nope", nil); code != 400 || out["error"] != "bad_request" {
		t.Fatalf("bad body: %d %v", code, out)
	}
}

func TestAdminCrossSiteWritesBlocked(t *testing.T) {
	e := newEnv(t)
	c := e.getAdminConfig(t)
	c["language"] = "fr"
	// A form post can't set a JSON content type.
	req, _ := http.NewRequest("PUT", e.srv.URL+"/api/admin/config", strings.NewReader(mustJSON(t, c)))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("text/plain: %d", resp.StatusCode)
	}
	for _, h := range []map[string]string{{"Origin": "http://evil.example"}, {"Sec-Fetch-Site": "cross-site"}} {
		if code, out := adminDo(t, "PUT", e.srv.URL+"/api/admin/config", c, h); code != 403 || out["error"] != "forbidden" {
			t.Fatalf("%v: %d %v", h, code, out)
		}
	}
	origin := map[string]string{"Origin": e.srv.URL, "Sec-Fetch-Site": "same-origin"}
	if code, out := adminDo(t, "PUT", e.srv.URL+"/api/admin/config", c, origin); code != 200 {
		t.Fatalf("same origin: %d %v", code, out)
	}
}

func TestAdminPassword(t *testing.T) {
	e := newEnv(t)
	srv := httptest.NewServer(e.server("hunter2").Handler())
	defer srv.Close()
	for _, p := range []string{"/settings", "/api/admin/config", "/api/admin/entities", "/api/admin/eccc-sites?q=tor"} {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 || !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Basic ") {
			t.Fatalf("%s without password: %d", p, resp.StatusCode)
		}
		req, _ := http.NewRequest("GET", srv.URL+p, nil)
		req.SetBasicAuth("x", "wrong")
		resp, _ = http.DefaultClient.Do(req)
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatalf("%s wrong password: %d", p, resp.StatusCode)
		}
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/admin/config", nil)
	req.SetBasicAuth("", "hunter2")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var c map[string]any
	json.NewDecoder(resp.Body).Decode(&c)
	resp.Body.Close()
	if resp.StatusCode != 200 || c["admin_password"] != true {
		t.Fatalf("right password: %d %v", resp.StatusCode, c)
	}
	// The Kindle's routes stay open.
	e.app.Refresh(context.Background())
	for _, p := range []string{"/healthz", "/frame.png?debug=1"} {
		resp, _ := http.Get(srv.URL + p)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", p, resp.StatusCode)
		}
	}
}

func TestAdminTestHA(t *testing.T) {
	e := newEnv(t)
	u := e.srv.URL + "/api/admin/test-ha"
	// Empty fields use the saved connection.
	code, out := adminDo(t, "POST", u, map[string]string{}, nil)
	if code != 200 || out["ok"] != true || out["entities"].(float64) != 10 {
		t.Fatalf("saved: %d %v", code, out) // 10 fake entities minus the sensor and lock, plus 2 shell commands
	}
	if code, out := adminDo(t, "POST", u, map[string]string{"token": "bad"}, nil); code != 502 || out["error"] != "ha_token_rejected" {
		t.Fatalf("bad token: %d %v", code, out)
	}
	if code, out := adminDo(t, "POST", u, map[string]string{"url": "ftp://x"}, nil); code != 400 || out["error"] != "invalid_url" {
		t.Fatalf("bad url: %d %v", code, out)
	}
	e.ha.setDown(true)
	if code, out := adminDo(t, "POST", u, map[string]string{}, nil); code != 502 || out["error"] != "ha_unreachable" {
		t.Fatalf("down: %d %v", code, out)
	}
	// Testing never saves.
	if e.store.Get().HA.Token != fakeToken {
		t.Fatal("test-ha changed the config")
	}
}

func TestAdminEntities(t *testing.T) {
	e := newEnv(t)
	code, out := adminDo(t, "GET", e.srv.URL+"/api/admin/entities", nil, nil)
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	var ids []string
	for _, x := range out["entities"].([]any) {
		ids = append(ids, x.(map[string]any)["id"].(string))
	}
	if strings.Join(ids, ",") != "cover.garage,input_boolean.guest_mode,light.kitchen,light.living_room,media_player.tv,scene.movie_night,"+
		"shell_command.monitor_off,shell_command.restart_kindle,switch.fan,switch.porch" {
		t.Fatalf("entities: %v", ids)
	}
	e.store.ClearToken()
	if code, out := adminDo(t, "GET", e.srv.URL+"/api/admin/entities", nil, nil); code != 409 || out["error"] != "ha_not_configured" {
		t.Fatalf("no token: %d %v", code, out)
	}
}

func siteCodes(t *testing.T, out map[string]any) []string {
	t.Helper()
	var s []string
	for _, x := range out["sites"].([]any) {
		m := x.(map[string]any)
		s = append(s, m["code"].(string)+":"+m["name"].(string))
	}
	return s
}

func TestAdminSiteSearch(t *testing.T) {
	e := newEnv(t)
	u := e.srv.URL + "/api/admin/eccc-sites"
	get := func(q string) []string {
		code, out := adminDo(t, "GET", u+q, nil, nil)
		if code != 200 {
			t.Fatalf("%s: %d %v", q, code, out)
		}
		return siteCodes(t, out)
	}
	// Accents and case don't matter; names starting with the query come first.
	if got := strings.Join(get("?q=montreal"), " "); got != "s0000635:Montréal s0000762:Montreal River Harbour" {
		t.Fatalf("montreal: %s", got)
	}
	if got := strings.Join(get("?q=TORONTO&province=on"), " "); got != "s0000458:Toronto s0000785:Toronto Island" {
		t.Fatalf("toronto: %s", got)
	}
	// Names in the chosen language; either language's name matches.
	if got := strings.Join(get("?q=cap-breton&lang=en"), " "); got != "s0000021:North Mountain (Cape Breton)" {
		t.Fatalf("fr name, en result: %s", got)
	}
	if got := strings.Join(get("?q=cape&lang=fr"), " "); got != "s0000021:North Mountain (Cap-Breton)" {
		t.Fatalf("en name, fr result: %s", got)
	}
	if got := strings.Join(get("?code=s0000620"), " "); got != "s0000620:Québec" {
		t.Fatalf("lookup: %s", got)
	}
	if got := get("?code=s9999999"); len(got) != 0 {
		t.Fatalf("unknown code: %v", got)
	}

	// The list is cached: a day passes before it is fetched again, and an
	// outage falls back to the copy on disk.
	e.dd.paths()
	get("?q=tor")
	if p := e.dd.paths(); len(p) != 0 {
		t.Fatalf("refetched within a day: %v", p)
	}
	e.dd.set(func(f *fakeDD) { f.fail = true })
	fresh := NewSiteList(e.sites.Dir)
	fresh.DD, fresh.Now = e.dd.URL, e.clk.Now
	got, err := fresh.Search(context.Background(), "québec", "", LangFR, 10)
	if err != nil || len(got) != 1 || got[0].Code != "s0000620" {
		t.Fatalf("offline from disk: %v %v", got, err)
	}
	empty := NewSiteList(t.TempDir())
	empty.DD = e.dd.URL
	if _, err := empty.Search(context.Background(), "x", "", LangEN, 10); err == nil {
		t.Fatal("no network and no cache should fail")
	}
	srv := httptest.NewServer((&Server{App: e.app, PollTimeout: time.Second, Renderer: NewRenderer(testIcon), Sites: empty}).Handler())
	defer srv.Close()
	if code, out := adminDo(t, "GET", srv.URL+"/api/admin/eccc-sites?q=x", nil, nil); code != 502 || out["error"] != "sites_unavailable" {
		t.Fatalf("unavailable: %d %v", code, out)
	}
}

func TestSettingsPage(t *testing.T) {
	e := newEnv(t)
	resp, err := http.Get(e.srv.URL + "/settings")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("settings: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	// Every error key the API can send has a translation in the page.
	keys := []string{"unauthorized", "forbidden", "bad_request", "config_conflict", "save_failed",
		"ha_not_configured", "ha_token_rejected", "ha_unreachable", "sites_unavailable"}
	for _, k := range []string{"invalid_url", "invalid_entity", "duplicate_entity", "invalid_language",
		"invalid_refresh", "invalid_rotation", "invalid_split", "invalid_columns", "invalid_flash", "invalid_scale"} {
		keys = append(keys, k)
	}
	page := string(b)
	for _, k := range keys {
		if n := strings.Count(page, "err_"+k+":"); n != 2 {
			t.Errorf("err_%s appears %d times, want 2 (en and fr)", k, n)
		}
	}
}

// Every ValidationError key in config.go must be one the page translates
// (TestSettingsPage checks the list; this keeps the list honest).
func TestValidationKeysListed(t *testing.T) {
	src, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := os.ReadFile("web/settings.html")
	for _, line := range strings.Split(string(src), "\n") {
		_, rest, ok := strings.Cut(line, `verr("`)
		if !ok {
			continue
		}
		key, _, _ := strings.Cut(rest, `"`)
		if !strings.Contains(string(page), "err_"+key+":") {
			t.Errorf("config.go key %q has no err_%s in settings.html", key, key)
		}
	}
}

func TestSetupModeRedirectsAndSeeds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	store, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, NewWeatherService(), NewIconCache(filepath.Join(dir, "icons")))
	srv := httptest.NewServer((&Server{App: app, PollTimeout: time.Second, Renderer: NewRenderer(testIcon), Sites: NewSiteList("")}).Handler())
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/settings" {
		t.Fatalf("setup /: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}

	env := map[string]string{"HA_URL": "http://10.0.0.5:8123/", "HA_TOKEN": "abc"}
	if err := seedConfig(store, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	if c := store.Get(); c.HA.URL != "http://10.0.0.5:8123" || c.HA.Token != "abc" || c.SetupMode() {
		t.Fatalf("seeded: %+v", c.HA)
	}
	// Once a config file exists the environment is ignored.
	env["HA_TOKEN"] = "other"
	if err := seedConfig(store, func(k string) string { return env[k] }); err != nil || store.Get().HA.Token != "abc" {
		t.Fatalf("re-seed: %v %q", err, store.Get().HA.Token)
	}
	// A bad URL fails loudly instead of starting with a broken config.
	bad, _ := LoadConfig(filepath.Join(t.TempDir(), "config.json"))
	if err := seedConfig(bad, func(k string) string { return map[string]string{"HA_URL": "ha.local"}[k] }); err == nil {
		t.Fatal("bad HA_URL accepted")
	}
}

func TestAdminPasswordFromEnv(t *testing.T) {
	f := filepath.Join(t.TempDir(), "pw")
	os.WriteFile(f, []byte("s3cret\n"), 0o600)
	env := map[string]string{"ADMIN_PASSWORD_FILE": f, "ADMIN_PASSWORD": "ignored"}
	if p, err := adminPassword(func(k string) string { return env[k] }); err != nil || p != "s3cret" {
		t.Fatalf("file: %q %v", p, err)
	}
	delete(env, "ADMIN_PASSWORD_FILE")
	if p, _ := adminPassword(func(k string) string { return env[k] }); p != "ignored" {
		t.Fatalf("env: %q", p)
	}
	env["ADMIN_PASSWORD_FILE"] = f + ".missing"
	if _, err := adminPassword(func(k string) string { return env[k] }); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestFrameRotationHeader(t *testing.T) {
	e := newEnv(t)
	e.store.Update(func(c *Config) error { c.Display.Rotation = 270; return nil })
	e.app.Refresh(context.Background())
	resp, err := http.Get(e.srv.URL + "/frame.png?debug=1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("X-Rotation") != "270" {
		t.Fatalf("X-Rotation %q", resp.Header.Get("X-Rotation"))
	}
}
