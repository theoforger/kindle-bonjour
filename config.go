package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Entity is one Home Assistant entity shown as a tile.
type Entity struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"` // empty: use HA's friendly_name
}

type HAConfig struct {
	URL   string `json:"url"`
	Token string `json:"token,omitempty"`
}

type ECCCConfig struct {
	Site           string `json:"site"`     // e.g. s0000458
	Province       string `json:"province"` // e.g. ON
	Lang           string `json:"lang"`     // en or fr
	RefreshSeconds int    `json:"refresh_seconds"`
	URLOverride    string `json:"url_override,omitempty"` // fixed XML URL, for testing
}

type DisplayConfig struct {
	Rotation     int     `json:"rotation"`      // 0, 90, 180, 270
	Split        float64 `json:"split"`         // left pane share of the width, 0.3..0.8
	Columns      int     `json:"columns"`       // tile columns, 1..4
	FlashMinutes int     `json:"flash_minutes"` // full refresh interval, 0 = never
	Clock        bool    `json:"clock"`
}

// Config is everything stored in config.json.
type Config struct {
	HA       HAConfig      `json:"home_assistant"`
	Entities []Entity      `json:"entities"`
	ECCC     ECCCConfig    `json:"eccc"`
	Display  DisplayConfig `json:"display"`
}

// DefaultConfig is used when there is no config file yet (setup mode).
func DefaultConfig() Config {
	return Config{
		Entities: []Entity{},
		ECCC:     ECCCConfig{Lang: "en", RefreshSeconds: 600},
		Display:  DisplayConfig{Rotation: 0, Split: 0.55, Columns: 2, FlashMinutes: 30, Clock: true},
	}
}

// SetupMode reports whether the config is too incomplete to reach HA.
func (c Config) SetupMode() bool { return c.HA.URL == "" || c.HA.Token == "" }

func (c Config) clone() Config {
	c.Entities = append([]Entity(nil), c.Entities...)
	if c.Entities == nil {
		c.Entities = []Entity{}
	}
	return c
}

// Validate normalizes c in place and rejects values the server can't use.
func (c *Config) Validate() error {
	c.HA.URL = strings.TrimRight(strings.TrimSpace(c.HA.URL), "/")
	if c.HA.URL != "" && !strings.HasPrefix(c.HA.URL, "http://") && !strings.HasPrefix(c.HA.URL, "https://") {
		return fmt.Errorf("home_assistant.url must start with http:// or https://")
	}
	seen := map[string]bool{}
	for i := range c.Entities {
		e := &c.Entities[i]
		e.ID = strings.TrimSpace(e.ID)
		e.Name = strings.TrimSpace(e.Name)
		if !validEntityID(e.ID) {
			return fmt.Errorf("invalid entity id %q", e.ID)
		}
		if seen[e.ID] {
			return fmt.Errorf("duplicate entity %q", e.ID)
		}
		seen[e.ID] = true
	}
	if c.Entities == nil {
		c.Entities = []Entity{}
	}
	c.ECCC.Province = strings.ToUpper(strings.TrimSpace(c.ECCC.Province))
	c.ECCC.Site = strings.TrimSpace(c.ECCC.Site)
	switch c.ECCC.Lang {
	case "":
		c.ECCC.Lang = "en"
	case "en", "fr":
	default:
		return fmt.Errorf("eccc.lang must be en or fr")
	}
	if c.ECCC.RefreshSeconds == 0 {
		c.ECCC.RefreshSeconds = 600
	}
	if c.ECCC.RefreshSeconds < 60 {
		return fmt.Errorf("eccc.refresh_seconds must be at least 60")
	}
	switch c.Display.Rotation {
	case 0, 90, 180, 270:
	default:
		return fmt.Errorf("display.rotation must be 0, 90, 180 or 270")
	}
	if c.Display.Split == 0 {
		c.Display.Split = 0.55
	}
	if c.Display.Split < 0.3 || c.Display.Split > 0.8 {
		return fmt.Errorf("display.split must be between 0.3 and 0.8")
	}
	if c.Display.Columns == 0 {
		c.Display.Columns = 2
	}
	if c.Display.Columns < 1 || c.Display.Columns > 4 {
		return fmt.Errorf("display.columns must be between 1 and 4")
	}
	if c.Display.FlashMinutes < 0 {
		return fmt.Errorf("display.flash_minutes must not be negative")
	}
	return nil
}

// validEntityID checks the "domain.object_id" shape HA uses.
func validEntityID(id string) bool {
	dom, obj, ok := strings.Cut(id, ".")
	if !ok || dom == "" || obj == "" {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '.') {
			return false
		}
	}
	return !strings.Contains(obj, ".")
}

// PublicConfig is the config as the admin API returns it: no token.
type PublicConfig struct {
	HA struct {
		URL      string `json:"url"`
		TokenSet bool   `json:"token_set"`
	} `json:"home_assistant"`
	Entities []Entity      `json:"entities"`
	ECCC     ECCCConfig    `json:"eccc"`
	Display  DisplayConfig `json:"display"`
	Version  uint64        `json:"version"`
}

// ConfigStore holds the config in memory and persists it atomically.
type ConfigStore struct {
	path string

	mu       sync.RWMutex
	cfg      Config
	version  uint64
	onChange func()
}

// LoadConfig reads path. A missing file gives the default (setup mode) config.
func LoadConfig(path string) (*ConfigStore, error) {
	s := &ConfigStore{path: path, cfg: DefaultConfig(), version: 1}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	cfg := DefaultConfig()
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s.cfg = cfg
	return s, nil
}

// OnChange registers a callback run (outside the lock) after every successful update.
func (s *ConfigStore) OnChange(fn func()) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

// Get returns a copy of the current config, token included (server-side use only).
func (s *ConfigStore) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.clone()
}

// Version increases by one on every saved change.
func (s *ConfigStore) Version() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version
}

// Public returns the config without the token.
func (s *ConfigStore) Public() PublicConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var p PublicConfig
	p.HA.URL = s.cfg.HA.URL
	p.HA.TokenSet = s.cfg.HA.Token != ""
	p.Entities = s.cfg.clone().Entities
	p.ECCC = s.cfg.ECCC
	p.Display = s.cfg.Display
	p.Version = s.version
	return p
}

// Update applies fn to a copy of the config, validates it, writes it to
// disk and only then makes it current. On any error nothing changes.
func (s *ConfigStore) Update(fn func(*Config) error) error {
	s.mu.Lock()
	next := s.cfg.clone()
	if err := fn(&next); err != nil {
		s.mu.Unlock()
		return err
	}
	if err := next.Validate(); err != nil {
		s.mu.Unlock()
		return err
	}
	if err := writeFileAtomic(s.path, next); err != nil {
		s.mu.Unlock()
		return err
	}
	s.cfg = next
	s.version++
	cb := s.onChange
	s.mu.Unlock()
	if cb != nil {
		cb()
	}
	return nil
}

// Replace stores in as the new config. The token is write-only: an empty
// token in `in` keeps the current one. Use ClearToken to remove it.
func (s *ConfigStore) Replace(in Config) error {
	return s.Update(func(c *Config) error {
		tok := c.HA.Token
		*c = in.clone()
		if c.HA.Token == "" {
			c.HA.Token = tok
		}
		return nil
	})
}

// ClearToken removes the stored HA token.
func (s *ConfigStore) ClearToken() error {
	return s.Update(func(c *Config) error { c.HA.Token = ""; return nil })
}

// writeFileAtomic writes cfg as JSON to a temp file (mode 0600) in the same
// directory, syncs it and renames it over path.
func writeFileAtomic(path string, cfg Config) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}
