package main

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testConfig(haURL string) Config {
	c := DefaultConfig()
	c.HA = HAConfig{URL: haURL, Token: fakeToken}
	c.Entities = []Entity{{ID: "light.living_room", Name: "Living Room"}, {ID: "switch.porch"}, {ID: "scene.movie_night"}}
	c.ECCC.Site, c.ECCC.Province = "s0000458", "ON"
	return c
}

func TestLoadMissingIsSetupMode(t *testing.T) {
	s, err := LoadConfig(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Get().SetupMode() {
		t.Fatal("missing config should be setup mode")
	}
	if s.Get().Entities == nil {
		t.Fatal("entities should be an empty list, not nil")
	}
}

func TestConfigPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	s, _ := LoadConfig(path)
	changes := 0
	s.OnChange(func() { changes++ })
	v0 := s.Version()
	if err := s.Replace(testConfig("http://ha.lan:8123/")); err != nil {
		t.Fatal(err)
	}
	if s.Version() != v0+1 || changes != 1 {
		t.Fatalf("version %d -> %d, %d change callbacks", v0, s.Version(), changes)
	}
	if got := s.Get().HA.URL; got != "http://ha.lan:8123" {
		t.Fatalf("url not normalized: %q", got)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("config mode %v, want 0600", fi.Mode().Perm())
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("leftover temp files: %v", ents)
	}

	re, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	a, b := s.Get(), re.Get()
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Fatalf("reload differs:\n%s\n%s", ja, jb)
	}
}

func TestConfigInvalidUpdateChangesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s, _ := LoadConfig(path)
	if err := s.Replace(testConfig("http://ha")); err != nil {
		t.Fatal(err)
	}
	v := s.Version()
	for name, bad := range map[string]func(*Config){
		"rotation":  func(c *Config) { c.Display.Rotation = 45 },
		"entity":    func(c *Config) { c.Entities = append(c.Entities, Entity{ID: "nodot"}) },
		"duplicate": func(c *Config) { c.Entities = append(c.Entities, c.Entities[0]) },
		"url":       func(c *Config) { c.HA.URL = "ftp://x" },
		"language":  func(c *Config) { c.Language = "de" },
	} {
		if err := s.Update(func(c *Config) error { bad(c); return nil }); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if s.Version() != v || s.Get().Display.Rotation != 0 || len(s.Get().Entities) != 3 {
		t.Fatal("rejected update changed the config")
	}
}

func TestLanguage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	s, _ := LoadConfig(path)
	if s.Get().Language != LangEN {
		t.Fatalf("default %q", s.Get().Language)
	}
	cfg := testConfig("http://ha")
	cfg.Language = " FR "
	if err := s.Replace(cfg); err != nil {
		t.Fatal(err)
	}
	if s.Get().Language != LangFR || s.Public().Language != LangFR {
		t.Fatal("language not saved")
	}
	if ws := s.Get().WeatherSource(); ws.Lang != LangFR {
		t.Fatalf("weather feed language %q", ws.Lang)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), `"lang"`) {
		t.Fatalf("eccc.lang written to disk: %s", b)
	}

	// Older configs kept the language in eccc.lang.
	os.WriteFile(path, []byte(`{"home_assistant":{"url":"http://ha","token":"t"},"eccc":{"site":"s1","province":"QC","lang":"fr"}}`), 0o600)
	old, err := LoadConfig(path)
	if err != nil || old.Get().Language != LangFR {
		t.Fatalf("migration: %v %q", err, old.Get().Language)
	}
	os.WriteFile(path, []byte(`{"home_assistant":{"url":"http://ha","token":"t"}}`), 0o600)
	if old, _ := LoadConfig(path); old.Get().Language != LangEN {
		t.Fatalf("missing language: %q", old.Get().Language)
	}
}

func TestTokenIsWriteOnly(t *testing.T) {
	s, _ := LoadConfig(filepath.Join(t.TempDir(), "config.json"))
	cfg := testConfig("http://ha")
	cfg.HA.Token = "s3cret-token-value"
	if err := s.Replace(cfg); err != nil {
		t.Fatal(err)
	}

	pub := s.Public()
	if !pub.HA.TokenSet {
		t.Fatal("token_set should be true")
	}
	j, _ := json.Marshal(pub)
	if strings.Contains(string(j), "s3cret") {
		t.Fatalf("public config leaks the token: %s", j)
	}

	// A settings save without a token keeps the stored one.
	cfg.HA.Token = ""
	cfg.Display.Columns = 3
	if err := s.Replace(cfg); err != nil {
		t.Fatal(err)
	}
	if got := s.Get(); got.HA.Token != "s3cret-token-value" || got.Display.Columns != 3 {
		t.Fatalf("token %q columns %d", got.HA.Token, got.Display.Columns)
	}

	// A new token replaces it; ClearToken removes it.
	cfg.HA.Token = "new"
	s.Replace(cfg)
	if s.Get().HA.Token != "new" {
		t.Fatal("token not replaced")
	}
	s.ClearToken()
	if s.Public().HA.TokenSet || !s.Get().SetupMode() {
		t.Fatal("token not cleared")
	}
}

func TestGetReturnsCopy(t *testing.T) {
	s, _ := LoadConfig(filepath.Join(t.TempDir(), "config.json"))
	s.Replace(testConfig("http://ha"))
	c := s.Get()
	c.Entities[0].ID = "light.changed"
	if s.Get().Entities[0].ID != "light.living_room" {
		t.Fatal("Get exposed internal slice")
	}
}

func TestScaleValidation(t *testing.T) {
	c := DefaultConfig()
	c.Display.Scale = Scale{Header: 0, Switches: 1.234, Weather: 0.5, Forecast: 2}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if want := (Scale{1, 1.23, 0.5, 2}); c.Display.Scale != want {
		t.Errorf("normalized to %+v, want %+v", c.Display.Scale, want)
	}
	for _, bad := range []struct {
		sc  Scale
		arg string
	}{
		{Scale{0.4, 1, 1, 1}, "header"},
		{Scale{1, 2.1, 1, 1}, "switches"},
		{Scale{1, 1, -1, 1}, "weather"},
		{Scale{1, 1, 1, math.NaN()}, "forecast"},
	} {
		c := DefaultConfig()
		c.Display.Scale = bad.sc
		var ve *ValidationError
		if err := c.Validate(); !errors.As(err, &ve) || ve.Key != "invalid_scale" || ve.Arg != bad.arg {
			t.Errorf("%+v: got %v, want invalid_scale %s", bad.sc, err, bad.arg)
		}
	}
}

func TestForecastValidation(t *testing.T) {
	c := DefaultConfig()
	c.Display.Forecast = ""
	if err := c.Validate(); err != nil || c.Display.Forecast != ForecastDaily {
		t.Errorf("empty: %v %q", err, c.Display.Forecast)
	}
	c.Display.Forecast = ForecastHourly
	if err := c.Validate(); err != nil || c.Display.Forecast != ForecastHourly {
		t.Errorf("hourly: %v %q", err, c.Display.Forecast)
	}
	c.Display.Forecast = "weekly"
	var ve *ValidationError
	if err := c.Validate(); !errors.As(err, &ve) || ve.Key != "invalid_forecast" {
		t.Errorf("weekly: %v", err)
	}
}
