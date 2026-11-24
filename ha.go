package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// actionServices are domains with no on/off state: a tap runs them.
var actionServices = map[string]string{
	"scene":        "scene/turn_on",
	"script":       "script/turn_on",
	"button":       "button/press",
	"input_button": "input_button/press",
}

// shellDomain is HA's shell_command integration. Its commands are services,
// not entities: a tile for one has the ID "shell_command.<name>", no state
// to read, and runs the command when tapped.
const shellDomain = "shell_command"

// shellTimeout is how long a shell command call may take. HA answers only
// when the command ends, and stops it after 60 s.
const shellTimeout = 65 * time.Second

// fallbackToggleDomains are offered in the entity picker if HA's service
// list can't be read. Normally the picker offers every domain that has a
// toggle service (see Entities).
var fallbackToggleDomains = map[string]bool{
	"light": true, "switch": true, "fan": true, "input_boolean": true,
	"automation": true, "siren": true, "humidifier": true, "media_player": true,
	"cover": true, "valve": true, "remote": true, "climate": true,
}

// notTileDomains have a toggle service but no entities worth a tile, or
// are handled as actions.
var notTileDomains = map[string]bool{"homeassistant": true, "script": true}

func entityDomain(id string) string {
	d, _, _ := strings.Cut(id, ".")
	return d
}

// IsAction reports whether tapping the entity runs it rather than toggling it.
func IsAction(id string) bool {
	d := entityDomain(id)
	_, ok := actionServices[d]
	return ok || d == shellDomain
}

// IsShellCommand reports whether the tile is a shell_command service.
func IsShellCommand(id string) bool { return entityDomain(id) == shellDomain }

// shellReady is the state given to shell command tiles while HA is reachable.
const shellReady = "ready"

// SwitchState is one tile's data.
type SwitchState struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	State  string `json:"state"` // raw HA state, "unavailable" on any error
	Action bool   `json:"action"`
}

// On reports whether the tile should be drawn as on (filled). Domains
// whose "on" isn't literally "on" are mapped here.
func (s SwitchState) On() bool {
	if s.Action || !s.Available() {
		return false
	}
	switch entityDomain(s.ID) {
	case "media_player":
		return s.State != "off" && s.State != "standby"
	case "cover", "valve":
		return s.State != "closed"
	case "climate", "water_heater":
		return s.State != "off"
	}
	return s.State == "on"
}

// StateKey is the i18n key of the tile's state text.
func (s SwitchState) StateKey() string {
	switch {
	case !s.Available():
		return "tile_na"
	case s.Action:
		return "tile_run"
	}
	switch entityDomain(s.ID) {
	case "media_player":
		switch s.State {
		case "playing":
			return "tile_playing"
		case "paused":
			return "tile_paused"
		}
	case "cover", "valve":
		if s.On() {
			return "tile_open"
		}
		return "tile_closed"
	}
	if s.On() {
		return "tile_on"
	}
	return "tile_off"
}

// Available reports whether HA gave a usable state.
func (s SwitchState) Available() bool {
	switch s.State {
	case "", "unavailable", "unknown":
		return false
	}
	return true
}

// EntityInfo is one row of the entity picker.
type EntityInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	State  string `json:"state"`
	Domain string `json:"domain"`
}

var ErrUnauthorized = errors.New("home assistant rejected the token")

// HAClient talks to the Home Assistant REST API.
type HAClient struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

func NewHAClient(cfg HAConfig) *HAClient {
	return &HAClient{BaseURL: strings.TrimRight(cfg.URL, "/"), Token: cfg.Token,
		Client: &http.Client{Timeout: 8 * time.Second}}
}

type haState struct {
	EntityID   string `json:"entity_id"`
	State      string `json:"state"`
	Attributes struct {
		FriendlyName string `json:"friendly_name"`
	} `json:"attributes"`
}

func (c *HAClient) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ErrUnauthorized
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("home assistant %s %s: %s", method, path, resp.Status)
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Ping checks the URL and token (GET /api/ returns {"message": "API running."}).
func (c *HAClient) Ping(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/api/", nil, nil)
}

// ReadStates fetches each entity's state (6 at a time) in config order.
// An entity that can't be read gets state "unavailable". The error is
// non-nil only if every read failed, so the caller can tell HA is down.
func (c *HAClient) ReadStates(ctx context.Context, ents []Entity) ([]SwitchState, error) {
	out := make([]SwitchState, len(ents))
	errs := make([]error, len(ents))
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	reads := 0
	for i, e := range ents {
		if IsShellCommand(e.ID) {
			// Nothing to read; marked below once HA's status is known.
			name := e.Name
			if name == "" {
				name = humanize(e.ID)
			}
			out[i] = SwitchState{ID: e.ID, Name: name, Action: true}
			continue
		}
		reads++
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			st := SwitchState{ID: e.ID, Name: e.Name, State: "unavailable", Action: IsAction(e.ID)}
			var s haState
			if err := c.do(ctx, http.MethodGet, "/api/states/"+url.PathEscape(e.ID), nil, &s); err != nil {
				errs[i] = err
			} else {
				st.State = s.State
				if st.Name == "" {
					st.Name = s.Attributes.FriendlyName
				}
			}
			if st.Name == "" {
				st.Name = e.ID
			}
			out[i] = st
		}()
	}
	wg.Wait()
	var err error
	switch {
	case len(ents) == 0:
	case reads == 0:
		err = c.Ping(ctx) // only shell commands: check HA is there at all
	default:
		// An error only if every read failed, so the caller can tell HA is down.
		for i, e := range errs {
			if IsShellCommand(ents[i].ID) {
				continue
			}
			if e == nil {
				err = nil
				break
			}
			if err == nil {
				err = e
			}
		}
	}
	for i := range out {
		if IsShellCommand(out[i].ID) {
			out[i].State = shellReady
			if err != nil {
				out[i].State = "unavailable"
			}
		}
	}
	return out, err
}

// humanize turns "shell_command.monitor_off" into "Monitor off".
func humanize(id string) string {
	_, obj, _ := strings.Cut(id, ".")
	obj = strings.TrimSpace(strings.ReplaceAll(obj, "_", " "))
	if obj == "" {
		return id
	}
	return strings.ToUpper(obj[:1]) + obj[1:]
}

// Toggle flips an on/off entity or runs an action entity. It does not check
// the entity is configured; App.Toggle does that.
func (c *HAClient) Toggle(ctx context.Context, id string) error {
	if IsShellCommand(id) {
		// A service, not an entity: no entity_id. HA answers when the
		// command ends, so allow longer than the usual client timeout.
		_, name, _ := strings.Cut(id, ".")
		long := *c
		hc := *c.Client
		hc.Timeout = shellTimeout
		long.Client = &hc
		return long.do(ctx, http.MethodPost, "/api/services/"+shellDomain+"/"+url.PathEscape(name), map[string]string{}, nil)
	}
	svc, ok := actionServices[entityDomain(id)]
	if !ok {
		// Works for every domain with a toggle service (media players,
		// covers, ...), and falls back to turn_on/turn_off otherwise.
		svc = "homeassistant/toggle"
	}
	return c.do(ctx, http.MethodPost, "/api/services/"+svc, map[string]string{"entity_id": id}, nil)
}

// haServices is GET /api/services: each domain's services by name.
type haServices []struct {
	Domain   string `json:"domain"`
	Services map[string]struct {
		Name string `json:"name"`
	} `json:"services"`
}

// tileDomains reads which domains have a toggle service, and the shell
// commands. On error the caller uses fallbackToggleDomains.
func (c *HAClient) tileDomains(ctx context.Context) (toggle map[string]bool, shell []EntityInfo, err error) {
	var svcs haServices
	if err := c.do(ctx, http.MethodGet, "/api/services", nil, &svcs); err != nil {
		return nil, nil, err
	}
	toggle = map[string]bool{}
	for _, d := range svcs {
		if d.Domain == shellDomain {
			for name, svc := range d.Services {
				id := shellDomain + "." + name
				if !validEntityID(id) {
					continue
				}
				label := svc.Name
				if label == "" {
					label = humanize(id)
				}
				shell = append(shell, EntityInfo{ID: id, Name: label, State: shellReady, Domain: shellDomain})
			}
			continue
		}
		if _, ok := d.Services["toggle"]; ok && !notTileDomains[d.Domain] {
			toggle[d.Domain] = true
		}
	}
	return toggle, shell, nil
}

// Entities lists the entities that can be put on a tile, sorted by ID.
func (c *HAClient) Entities(ctx context.Context) ([]EntityInfo, error) {
	var all []haState
	if err := c.do(ctx, http.MethodGet, "/api/states", nil, &all); err != nil {
		return nil, err
	}
	toggle, shell, err := c.tileDomains(ctx)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			return nil, err
		}
		toggle = fallbackToggleDomains
	}
	out := shell
	if out == nil {
		out = []EntityInfo{}
	}
	for _, s := range all {
		d := entityDomain(s.EntityID)
		if !toggle[d] && actionServices[d] == "" {
			continue
		}
		name := s.Attributes.FriendlyName
		if name == "" {
			name = s.EntityID
		}
		out = append(out, EntityInfo{ID: s.EntityID, Name: name, State: s.State, Domain: d})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
