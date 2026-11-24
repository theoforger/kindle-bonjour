package main

import (
	"encoding/json"
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
		"lang":      func(c *Config) { c.ECCC.Lang = "de" },
	} {
		if err := s.Update(func(c *Config) error { bad(c); return nil }); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if s.Version() != v || s.Get().Display.Rotation != 0 || len(s.Get().Entities) != 3 {
		t.Fatal("rejected update changed the config")
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
