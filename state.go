package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"
)

// Snapshot is everything visible on a frame. The version goes up only when
// a new snapshot differs from the current one.
type Snapshot struct {
	Setup          bool          `json:"setup"`
	Switches       []SwitchState `json:"switches"`
	HAError        string        `json:"ha_error,omitempty"`
	Weather        *Weather      `json:"weather"`
	WeatherStale   bool          `json:"weather_stale"`
	WeatherUpdated string        `json:"weather_updated,omitempty"` // local HH:MM of last good fetch
	Clock          string        `json:"clock,omitempty"`           // HH:MM, empty when the clock is off
	Date           string        `json:"date,omitempty"`
	Display        DisplayConfig `json:"display"`
}

// Hub holds the current snapshot and its version, and wakes long-polls.
type Hub struct {
	mu      sync.Mutex
	version uint64
	snap    Snapshot
	key     []byte
	changed chan struct{}
}

// NewHub starts versions at a time-based value so a Kindle still holding a
// version from before a server restart never matches by accident.
func NewHub(now time.Time) *Hub {
	return &Hub{version: uint64(now.UnixMilli()), changed: make(chan struct{})}
}

// Publish makes s current. It bumps the version and wakes waiters only if
// s differs from the current snapshot, and returns the current version.
func (h *Hub) Publish(s Snapshot) uint64 {
	key, err := json.Marshal(s)
	if err != nil {
		panic(err) // Snapshot is plain data
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.key != nil && bytes.Equal(key, h.key) {
		return h.version
	}
	h.key, h.snap = key, s
	h.version++
	close(h.changed)
	h.changed = make(chan struct{})
	return h.version
}

// Current returns the snapshot and version. ok is false before the first Publish.
func (h *Hub) Current() (s Snapshot, version uint64, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snap, h.version, h.key != nil
}

// Wait returns as soon as there is a published version other than since,
// or reports ok=false when ctx ends first.
func (h *Hub) Wait(ctx context.Context, since uint64) (s Snapshot, version uint64, ok bool) {
	for {
		h.mu.Lock()
		if h.key != nil && h.version != since {
			s, version = h.snap, h.version
			h.mu.Unlock()
			return s, version, true
		}
		ch := h.changed
		h.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return Snapshot{}, 0, false
		}
	}
}

var (
	ErrUnknownEntity = errors.New("entity is not on the dashboard")
	ErrSetupMode     = errors.New("home assistant is not configured")
)

// App ties config, Home Assistant and weather together and publishes snapshots.
type App struct {
	Config  *ConfigStore
	Weather *WeatherService
	Icons   *IconCache
	Hub     *Hub
	Now     func() time.Time
	HAPoll  time.Duration // how often HA states are read

	refreshMu sync.Mutex
	trigger   chan struct{}

	mu      sync.Mutex
	haErr   error
	haOKAt  time.Time
	afterOp time.Duration // delay before the re-read after a toggle
}

func NewApp(cfg *ConfigStore, w *WeatherService, icons *IconCache) *App {
	a := &App{Config: cfg, Weather: w, Icons: icons, Hub: NewHub(time.Now()), Now: time.Now,
		HAPoll: 3 * time.Second, trigger: make(chan struct{}, 1), afterOp: 700 * time.Millisecond}
	cfg.OnChange(a.Trigger)
	return a
}

// Trigger asks the run loop to refresh soon. It never blocks.
func (a *App) Trigger() {
	select {
	case a.trigger <- struct{}{}:
	default:
	}
}

// Refresh reads HA, takes the cached weather and the clock, and publishes.
func (a *App) Refresh(ctx context.Context) uint64 {
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()
	cfg := a.Config.Get()
	now := a.Now()
	s := Snapshot{Setup: cfg.SetupMode(), Display: cfg.Display, Switches: []SwitchState{}}
	if !s.Setup {
		rctx, cancel := context.WithTimeout(ctx, 6*time.Second)
		sw, err := NewHAClient(cfg.HA).ReadStates(rctx, cfg.Entities)
		cancel()
		s.Switches = sw
		a.mu.Lock()
		if err != nil && ctx.Err() == nil {
			if a.haErr == nil || a.haErr.Error() != err.Error() {
				log.Printf("home assistant: %v", err)
			}
			a.haErr = err
			s.HAError = "Home Assistant unreachable"
			if errors.Is(err, ErrUnauthorized) {
				s.HAError = "Home Assistant token rejected"
			}
		} else if err == nil {
			a.haErr, a.haOKAt = nil, now
		}
		a.mu.Unlock()
	}
	ws := a.Weather.Status()
	s.Weather, s.WeatherStale = ws.Weather, ws.Stale
	if !ws.FetchedAt.IsZero() {
		s.WeatherUpdated = ws.FetchedAt.In(now.Location()).Format("15:04")
	}
	if cfg.Display.Clock {
		s.Clock = now.Format("15:04")
		s.Date = now.Format("Mon Jan 2")
	}
	return a.Hub.Publish(s)
}

// Run refreshes on every HA poll tick, on triggers and at each minute
// boundary, until ctx ends. It also runs the weather loop.
func (a *App) Run(ctx context.Context) {
	go a.Weather.Run(ctx, func() ECCCConfig { return a.Config.Get().ECCC }, a.Trigger)
	tick := time.NewTicker(a.HAPoll)
	defer tick.Stop()
	for {
		a.Refresh(ctx)
		now := a.Now()
		minute := time.NewTimer(now.Truncate(time.Minute).Add(time.Minute).Sub(now) + 50*time.Millisecond)
		select {
		case <-ctx.Done():
			minute.Stop()
			return
		case <-tick.C:
		case <-a.trigger:
		case <-minute.C:
		}
		minute.Stop()
	}
}

// Toggle flips or runs a configured entity. Unlisted entities are rejected
// without contacting HA.
func (a *App) Toggle(ctx context.Context, id string) error {
	cfg := a.Config.Get()
	found := false
	for _, e := range cfg.Entities {
		if e.ID == id {
			found = true
			break
		}
	}
	if !found {
		return ErrUnknownEntity
	}
	if cfg.SetupMode() {
		return ErrSetupMode
	}
	if err := NewHAClient(cfg.HA).Toggle(ctx, id); err != nil {
		return err
	}
	a.Trigger()
	// HA often reports the new state a moment later; read again then.
	time.AfterFunc(a.afterOp, a.Trigger)
	return nil
}

// HAStatus reports the last HA error and the last successful read.
func (a *App) HAStatus() (lastErr error, okAt time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.haErr, a.haOKAt
}
