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

// toggleDomains are the on/off domains offered in the entity picker.
var toggleDomains = map[string]bool{
	"light": true, "switch": true, "fan": true, "input_boolean": true,
	"automation": true, "siren": true, "humidifier": true,
}

func entityDomain(id string) string {
	d, _, _ := strings.Cut(id, ".")
	return d
}

// IsAction reports whether tapping the entity runs it rather than toggling it.
func IsAction(id string) bool { _, ok := actionServices[entityDomain(id)]; return ok }

// SwitchState is one tile's data.
type SwitchState struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	State  string `json:"state"` // raw HA state, "unavailable" on any error
	Action bool   `json:"action"`
}

// On reports whether the tile should be drawn as ON.
func (s SwitchState) On() bool { return !s.Action && s.State == "on" }

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
	for i, e := range ents {
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
	if len(ents) == 0 {
		return out, nil
	}
	for _, err := range errs {
		if err == nil {
			return out, nil
		}
	}
	return out, errs[0]
}

// Toggle flips an on/off entity or runs an action entity. It does not check
// the entity is configured; App.Toggle does that.
func (c *HAClient) Toggle(ctx context.Context, id string) error {
	svc, ok := actionServices[entityDomain(id)]
	if !ok {
		svc = "homeassistant/toggle"
	}
	return c.do(ctx, http.MethodPost, "/api/services/"+svc, map[string]string{"entity_id": id}, nil)
}

// Entities lists the entities that can be put on a tile, sorted by ID.
func (c *HAClient) Entities(ctx context.Context) ([]EntityInfo, error) {
	var all []haState
	if err := c.do(ctx, http.MethodGet, "/api/states", nil, &all); err != nil {
		return nil, err
	}
	out := []EntityInfo{}
	for _, s := range all {
		d := entityDomain(s.EntityID)
		if !toggleDomains[d] && actionServices[d] == "" {
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
