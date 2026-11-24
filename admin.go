package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"log"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The admin API and the settings page. Errors are JSON
// {"ok": false, "error": "<message key>", "arg": "..."}; the settings page
// translates the key (see adminErrorKeys in web/settings.html).

func adminError(w http.ResponseWriter, code int, key, arg string) {
	out := map[string]any{"ok": false, "error": key}
	if arg != "" {
		out["arg"] = arg
	}
	writeJSON(w, code, out)
}

// admin wraps a settings handler with the optional password and, for
// requests that change something, a same-origin check.
func (s *Server) admin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.AdminPassword != "" && !s.passwordOK(r) {
			w.Header().Set("WWW-Authenticate", `Basic realm="kindle-bonjour settings", charset="UTF-8"`)
			adminError(w, http.StatusUnauthorized, "unauthorized", "")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
			adminError(w, http.StatusForbidden, "forbidden", "")
			return
		}
		h(w, r)
	}
}

// passwordOK checks HTTP Basic auth. Any user name is accepted.
func (s *Server) passwordOK(r *http.Request) bool {
	_, pw, ok := r.BasicAuth()
	if !ok {
		return false
	}
	a, b := sha256.Sum256([]byte(pw)), sha256.Sum256([]byte(s.AdminPassword))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// sameOrigin blocks cross-site writes: the body must be JSON (so a plain
// HTML form can't send it without a CORS preflight, which we never answer)
// and the Origin, if sent, must be this host.
func sameOrigin(r *http.Request) bool {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Host != r.Host {
			return false
		}
	}
	return r.Header.Get("Sec-Fetch-Site") != "cross-site"
}

func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
}

//go:embed web/settings.html
var settingsHTML []byte

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Write(settingsHTML)
}

// adminConfig is what GET /api/admin/config returns.
type adminConfig struct {
	PublicConfig
	Setup         bool `json:"setup"`
	AdminPassword bool `json:"admin_password"` // a password protects these pages
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.App.Config
	writeJSON(w, http.StatusOK, adminConfig{PublicConfig: cfg.Public(), Setup: cfg.Get().SetupMode(),
		AdminPassword: s.AdminPassword != ""})
}

// configIn is the body of PUT /api/admin/config: the whole config. An
// empty token keeps the stored one unless clear_token is set. A non-zero
// version must match the current one, so two open settings pages can't
// silently overwrite each other.
type configIn struct {
	Language string `json:"language"`
	HA       struct {
		URL        string `json:"url"`
		Token      string `json:"token"`
		ClearToken bool   `json:"clear_token"`
	} `json:"home_assistant"`
	Entities []Entity      `json:"entities"`
	ECCC     ECCCConfig    `json:"eccc"`
	Display  DisplayConfig `json:"display"`
	Version  uint64        `json:"version"`
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	var in configIn
	if err := decodeJSON(r, &in); err != nil {
		adminError(w, http.StatusBadRequest, "bad_request", "")
		return
	}
	cfg := Config{Language: in.Language, HA: HAConfig{URL: in.HA.URL, Token: strings.TrimSpace(in.HA.Token)},
		Entities: in.Entities, ECCC: in.ECCC, Display: in.Display}
	err := s.App.Config.ReplaceIf(cfg, in.Version, in.HA.ClearToken)
	var ve *ValidationError
	switch {
	case err == nil:
		s.getConfig(w, r)
	case errors.As(err, &ve):
		adminError(w, http.StatusBadRequest, ve.Key, ve.Arg)
	case errors.Is(err, ErrConfigConflict):
		adminError(w, http.StatusConflict, "config_conflict", "")
	default:
		log.Printf("save config: %v", err)
		adminError(w, http.StatusInternalServerError, "save_failed", "")
	}
}

// haClientFor builds a client from a request's URL and token, filling
// either from the saved config when it is empty.
func (s *Server) haClientFor(urlIn, token string) (*HAClient, string) {
	saved := s.App.Config.Get().HA
	c := HAConfig{URL: strings.TrimRight(strings.TrimSpace(urlIn), "/"), Token: strings.TrimSpace(token)}
	if c.URL == "" {
		c.URL = saved.URL
	}
	if c.Token == "" {
		c.Token = saved.Token
	}
	switch {
	case c.URL == "" || c.Token == "":
		return nil, "ha_not_configured"
	case !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://"):
		return nil, "invalid_url"
	}
	return NewHAClient(c), ""
}

// haErrorKey maps an HA client error to a message key.
func haErrorKey(err error) string {
	if errors.Is(err, ErrUnauthorized) {
		return "ha_token_rejected"
	}
	return "ha_unreachable"
}

// testHA handles POST /api/admin/test-ha {"url", "token"}: it checks the
// connection without saving anything. Empty fields use the saved values.
func (s *Server) testHA(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &in); err != nil {
		adminError(w, http.StatusBadRequest, "bad_request", "")
		return
	}
	c, key := s.haClientFor(in.URL, in.Token)
	if c == nil {
		adminError(w, http.StatusBadRequest, key, "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		adminError(w, http.StatusBadGateway, haErrorKey(err), err.Error())
		return
	}
	n := 0
	if ents, err := c.Entities(ctx); err == nil {
		n = len(ents)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "entities": n})
}

// entities handles GET /api/admin/entities: the entities that can go on a
// tile, read with the saved connection.
func (s *Server) entities(w http.ResponseWriter, r *http.Request) {
	c, key := s.haClientFor("", "")
	if c == nil {
		adminError(w, http.StatusConflict, key, "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	ents, err := c.Entities(ctx)
	if err != nil {
		adminError(w, http.StatusBadGateway, haErrorKey(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "entities": ents})
}

type siteOut struct {
	Site
	Name string `json:"name"` // in the requested language
}

// ecccSites handles GET /api/admin/eccc-sites?q=&province=&lang=, or
// ?code= to look up one site (to show the saved one by name).
func (s *Server) ecccSites(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lang := q.Get("lang")
	if lang != LangFR && lang != LangEN {
		lang = s.App.Config.Get().Language
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var sites []Site
	var err error
	if code := q.Get("code"); code != "" {
		var site Site
		var ok bool
		site, ok, err = s.Sites.Lookup(ctx, code)
		if ok {
			sites = []Site{site}
		}
	} else {
		sites, err = s.Sites.Search(ctx, q.Get("q"), q.Get("province"), lang, 30)
	}
	if err != nil {
		adminError(w, http.StatusBadGateway, "sites_unavailable", err.Error())
		return
	}
	out := []siteOut{}
	for _, st := range sites {
		out = append(out, siteOut{st, st.Name(lang)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sites": out})
}
