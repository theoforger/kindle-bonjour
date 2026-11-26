package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"sort"
	"sync"
	"time"
)

// Snapshot is everything visible on a frame. The version goes up only when
// a new snapshot differs from the current one.
type Snapshot struct {
	Language       string        `json:"language"` // en or fr; the renderer looks up text with T
	Setup          bool          `json:"setup"`
	Switches       []SwitchState `json:"switches"`
	HAError        string        `json:"ha_error,omitempty"` // message key, e.g. ha_unreachable
	Weather        *Weather      `json:"weather"`
	WeatherStale   bool          `json:"weather_stale"`
	WeatherUpdated string        `json:"weather_updated,omitempty"`  // local time of last good fetch, localized
	WeatherObs     string        `json:"weather_observed,omitempty"` // local time of the observation, localized
	Clock          string        `json:"clock,omitempty"`            // localized time, empty when the clock is off
	Date           string        `json:"date,omitempty"`
	Display        DisplayConfig `json:"display"`
	Pending        []string      `json:"pending,omitempty"` // tapped tiles waiting for HA (dotted border)
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

	// Pending taps. pubMu makes "build snapshot + publish" atomic, so a
	// tap's quick republish and a slower Refresh never overtake each other.
	pubMu   sync.Mutex
	last    Snapshot // last refreshed snapshot, without Pending
	hasLast bool
	pending map[string]*pendingTap
}

// pendingTap is a tile that was tapped and hasn't been confirmed by HA yet.
type pendingTap struct {
	since  time.Time
	prev   string    // HA state when tapped
	doneAt time.Time // when HA accepted the call; zero while in flight
}

// How long a tile stays pending: until HA reports a new state, or this
// long after HA accepted the call, or at most pendingMax after the tap.
const (
	pendingAfterDone = 3 * time.Second
	pendingMax       = 10 * time.Second
)

func NewApp(cfg *ConfigStore, w *WeatherService, icons *IconCache) *App {
	a := &App{Config: cfg, Weather: w, Icons: icons, Hub: NewHub(time.Now()), Now: time.Now,
		HAPoll: 3 * time.Second, trigger: make(chan struct{}, 1), afterOp: 700 * time.Millisecond,
		pending: map[string]*pendingTap{}}
	cfg.OnChange(func() { a.Weather.Wake(); a.Trigger() })
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
	lang := cfg.Language
	s := Snapshot{Language: lang, Setup: cfg.SetupMode(), Display: cfg.Display, Switches: []SwitchState{}}
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
			s.HAError = "ha_unreachable"
			if errors.Is(err, ErrUnauthorized) {
				s.HAError = "ha_token_rejected"
			}
		} else if err == nil {
			a.haErr, a.haOKAt = nil, now
		}
		a.mu.Unlock()
	}
	ws := a.Weather.Status(cfg.WeatherSource())
	s.Weather, s.WeatherStale = ws.Weather.Local(now), ws.Stale
	if s.Weather != nil && !s.Weather.Observed.IsZero() {
		s.WeatherObs = formatClock(lang, s.Weather.Observed.In(now.Location()))
	}
	if !ws.FetchedAt.IsZero() {
		s.WeatherUpdated = formatClock(lang, ws.FetchedAt.In(now.Location()))
	}
	if cfg.Display.Clock {
		s.Clock = formatClock(lang, now)
		s.Date = formatDate(lang, now)
	}
	return a.publish(s, true)
}

// publish adds the pending taps to s and publishes it. fresh means s has
// new HA states, so confirmed or expired taps are cleared first.
func (a *App) publish(s Snapshot, fresh bool) uint64 {
	a.pubMu.Lock()
	defer a.pubMu.Unlock()
	now := a.Now()
	a.mu.Lock()
	states := map[string]SwitchState{}
	for _, sw := range s.Switches {
		states[sw.ID] = sw
	}
	s.Pending = nil
	for id, p := range a.pending {
		sw, listed := states[id]
		switch {
		case !listed, now.Sub(p.since) > pendingLimit(id, p):
			delete(a.pending, id)
			continue
		case fresh && !p.doneAt.IsZero() && (sw.Action || sw.State != p.prev || now.Sub(p.doneAt) > pendingAfterDone):
			delete(a.pending, id)
			continue
		}
		s.Pending = append(s.Pending, id)
	}
	a.mu.Unlock()
	sort.Strings(s.Pending)
	if fresh {
		s0 := s
		s0.Pending = nil
		a.last, a.hasLast = s0, true
	}
	return a.Hub.Publish(s)
}

// pendingLimit is how long a tap may stay pending. A shell command keeps
// its border until HA answers, since HA answers only when it ends.
func pendingLimit(id string, p *pendingTap) time.Duration {
	if IsShellCommand(id) && p.doneAt.IsZero() {
		return shellTimeout
	}
	return pendingMax
}

// republish publishes the last snapshot again with the current pending set.
func (a *App) republish() {
	a.pubMu.Lock()
	s, ok := a.last, a.hasLast
	a.pubMu.Unlock()
	if ok {
		a.publish(s, false)
	}
}

// Run refreshes on every HA poll tick, on triggers and at each minute
// boundary, until ctx ends. It also runs the weather loop.
func (a *App) Run(ctx context.Context) {
	go a.Weather.Run(ctx, func() ECCCConfig { return a.Config.Get().WeatherSource() }, a.Trigger)
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
	if !hasEntity(cfg, id) {
		return ErrUnknownEntity
	}
	if cfg.SetupMode() {
		return ErrSetupMode
	}
	if err := NewHAClient(cfg.HA).Toggle(ctx, id); err != nil {
		return err
	}
	a.refreshSoon()
	return nil
}

// refreshSoon re-reads HA now and again a moment later, since HA often
// reports the new state only after the call returns.
func (a *App) refreshSoon() {
	a.Trigger()
	time.AfterFunc(a.afterOp, a.Trigger)
}

// Tap marks a configured entity pending, shows that at once, and toggles
// it in the background. Errors are those of Toggle that can be known
// before contacting HA.
func (a *App) Tap(id string) error {
	cfg := a.Config.Get()
	if !hasEntity(cfg, id) {
		return ErrUnknownEntity
	}
	if cfg.SetupMode() {
		return ErrSetupMode
	}
	a.pubMu.Lock()
	prev := ""
	for _, sw := range a.last.Switches {
		if sw.ID == id {
			prev = sw.State
		}
	}
	a.pubMu.Unlock()
	a.mu.Lock()
	if p, ok := a.pending[id]; ok && p.doneAt.IsZero() {
		a.mu.Unlock()
		return nil // the first tap is still in flight; ignore the repeat
	}
	a.pending[id] = &pendingTap{since: a.Now(), prev: prev}
	a.mu.Unlock()
	a.republish()

	go func() {
		limit := 10 * time.Second
		if IsShellCommand(id) {
			limit = shellTimeout // HA answers when the command ends
		}
		ctx, cancel := context.WithTimeout(context.Background(), limit)
		defer cancel()
		// Not Toggle: doneAt must be set before the re-reads, or the first
		// one can't clear the pending border.
		err := NewHAClient(cfg.HA).Toggle(ctx, id)
		a.mu.Lock()
		if p, ok := a.pending[id]; ok {
			if err != nil {
				delete(a.pending, id)
			} else {
				p.doneAt = a.Now()
			}
		}
		a.mu.Unlock()
		if err != nil {
			log.Printf("tap %s: %v", id, err)
			a.republish()
			return
		}
		a.refreshSoon()
	}()
	return nil
}

func hasEntity(cfg Config, id string) bool {
	for _, e := range cfg.Entities {
		if e.ID == id {
			return true
		}
	}
	return false
}

// HAStatus reports the last HA error and the last successful read.
func (a *App) HAStatus() (lastErr error, okAt time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.haErr, a.haOKAt
}
