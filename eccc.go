package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultDD      = "https://dd.weather.gc.ca"
	defaultIconURL = "https://weather.gc.ca/weathericons/"
	userAgent      = "kindle-bonjour/1.0"
	retryAfterFail = 60 * time.Second
)

// Weather is the parsed citypage, reduced to what the frame shows.
type Weather struct {
	Location   string   `json:"location"`
	Station    string   `json:"station"`
	Observed   string   `json:"observed"` // local time, e.g. "13:00 EST" or "13:00 HNE"
	Condition  string   `json:"condition"`
	Icon       string   `json:"icon"`
	Temp       string   `json:"temp"`
	Feels      string   `json:"feels"` // wind chill or humidex
	Humidity   string   `json:"humidity"`
	Pressure   string   `json:"pressure"`
	Tendency   string   `json:"tendency"`
	Visibility string   `json:"visibility"`
	Wind       Wind     `json:"wind"`
	Warnings   []string `json:"warnings"`
	Forecast   []Period `json:"forecast"`
}

// Wind is kept as parts; the frame adds the localized labels (see WindText).
type Wind struct {
	Direction string `json:"direction"` // as sent: WNW in English, ONO in French
	Speed     string `json:"speed"`     // km/h, or "calm"/"calme"
	Gust      string `json:"gust"`
}

// Calm reports whether there is no wind to show a speed for.
func (w Wind) Calm() bool {
	switch strings.ToLower(w.Speed) {
	case "calm", "calme", "0", "00":
		return true
	}
	return false
}

type Period struct {
	Name    string `json:"name"`
	Icon    string `json:"icon"`
	Summary string `json:"summary"`
	POP     string `json:"pop"`
	Temp    string `json:"temp"`
	Class   string `json:"class"` // high or low
}

// XML shapes. encoding/xml skips any element not named here, so new
// fields in the feed don't break parsing.
type xSiteData struct {
	Location struct {
		Name string `xml:"name"`
	} `xml:"location"`
	Warnings struct {
		Events []struct {
			Description string `xml:"description,attr"`
		} `xml:"event"`
	} `xml:"warnings"`
	Current *struct {
		Station   string `xml:"station"`
		DateTimes []struct {
			Name    string `xml:"name,attr"`
			Zone    string `xml:"zone,attr"`
			Hour    string `xml:"hour"`
			Minute  string `xml:"minute"`
			Summary string `xml:"textSummary"`
		} `xml:"dateTime"`
		Condition string `xml:"condition"`
		IconCode  string `xml:"iconCode"`
		Temp      string `xml:"temperature"`
		WindChill string `xml:"windChill"`
		Humidex   string `xml:"humidex"`
		Humidity  string `xml:"relativeHumidity"`
		Pressure  struct {
			Value    string `xml:",chardata"`
			Tendency string `xml:"tendency,attr"`
		} `xml:"pressure"`
		Visibility string `xml:"visibility"`
		Wind       struct {
			Speed     string `xml:"speed"`
			Gust      string `xml:"gust"`
			Direction string `xml:"direction"`
		} `xml:"wind"`
	} `xml:"currentConditions"`
	Forecasts []struct {
		Period struct {
			Name  string `xml:"textForecastName,attr"`
			Value string `xml:",chardata"`
		} `xml:"period"`
		Abbr struct {
			IconCode string `xml:"iconCode"`
			POP      string `xml:"pop"`
			Summary  string `xml:"textSummary"`
		} `xml:"abbreviatedForecast"`
		Temps []struct {
			Value string `xml:",chardata"`
			Class string `xml:"class,attr"`
		} `xml:"temperatures>temperature"`
	} `xml:"forecastGroup>forecast"`
}

// ParseCitypage parses an ECCC citypage XML document.
func ParseCitypage(data []byte) (*Weather, error) {
	var x xSiteData
	d := xml.NewDecoder(bytes.NewReader(data))
	d.CharsetReader = charsetReader
	if err := d.Decode(&x); err != nil {
		return nil, fmt.Errorf("parse citypage: %w", err)
	}
	t := strings.TrimSpace
	w := &Weather{Location: t(x.Location.Name), Warnings: []string{}, Forecast: []Period{}}
	if c := x.Current; c != nil {
		w.Station = t(c.Station)
		for _, dt := range c.DateTimes {
			if dt.Name != "observation" || dt.Zone == "" || dt.Zone == "UTC" {
				continue
			}
			if h, m := t(dt.Hour), t(dt.Minute); h != "" && m != "" {
				w.Observed = h + ":" + m + " " + dt.Zone
				continue
			}
			// No structured time: use the text, e.g. "Monday ... at 13:00 EST".
			s := t(dt.Summary)
			if _, after, ok := strings.Cut(s, " at "); ok {
				s = after
			}
			w.Observed = s
		}
		w.Condition = t(c.Condition)
		w.Icon = t(c.IconCode)
		w.Temp = t(c.Temp)
		w.Feels = t(c.WindChill)
		if w.Feels == "" {
			w.Feels = t(c.Humidex)
		}
		w.Humidity = t(c.Humidity)
		w.Pressure = t(c.Pressure.Value)
		w.Tendency = t(c.Pressure.Tendency)
		w.Visibility = t(c.Visibility)
		w.Wind = Wind{Direction: t(c.Wind.Direction), Speed: t(c.Wind.Speed), Gust: t(c.Wind.Gust)}
	}
	for _, f := range x.Forecasts {
		p := Period{Name: t(f.Period.Name), Icon: t(f.Abbr.IconCode), Summary: t(f.Abbr.Summary), POP: t(f.Abbr.POP)}
		if p.Name == "" {
			p.Name = t(f.Period.Value)
		}
		if len(f.Temps) > 0 {
			p.Temp, p.Class = t(f.Temps[0].Value), t(f.Temps[0].Class)
		}
		w.Forecast = append(w.Forecast, p)
	}
	for _, ev := range x.Warnings.Events {
		if s := t(ev.Description); s != "" {
			w.Warnings = append(w.Warnings, s)
		}
	}
	return w, nil
}

// charsetReader handles the ISO-8859-1 that older ECCC files declare.
func charsetReader(label string, in io.Reader) (io.Reader, error) {
	switch strings.ToLower(label) {
	case "utf-8", "utf8":
		return in, nil
	case "iso-8859-1", "latin1", "latin-1", "windows-1252":
		return &latin1Reader{r: bufio.NewReader(in)}, nil
	}
	return nil, fmt.Errorf("unsupported charset %q", label)
}

type latin1Reader struct {
	r   *bufio.Reader
	buf []byte
}

func (l *latin1Reader) Read(p []byte) (int, error) {
	for len(l.buf) < len(p) {
		b, err := l.r.ReadByte()
		if err != nil {
			if len(l.buf) == 0 {
				return 0, err
			}
			break
		}
		l.buf = append(l.buf, string(rune(b))...)
	}
	n := copy(p, l.buf)
	l.buf = l.buf[n:]
	return n, nil
}

// WeatherStatus is a snapshot of the weather cache.
type WeatherStatus struct {
	Weather   *Weather  `json:"weather"`
	Stale     bool      `json:"stale"` // last refresh failed or data is too old
	FetchedAt time.Time `json:"fetched_at"`
	Err       string    `json:"error,omitempty"`
}

// WeatherService fetches and caches one citypage.
type WeatherService struct {
	DD     string // datamart base URL
	Client *http.Client
	Now    func() time.Time

	refreshMu sync.Mutex    // one refresh at a time
	wake      chan struct{} // makes Run check again now

	mu        sync.Mutex
	key       string // config the cache belongs to
	ttl       time.Duration
	data      *Weather
	fetchedAt time.Time
	attemptAt time.Time
	lastErr   error
}

func NewWeatherService() *WeatherService {
	return &WeatherService{DD: defaultDD, Client: &http.Client{Timeout: 15 * time.Second}, Now: time.Now,
		wake: make(chan struct{}, 1)}
}

func ecccKey(c ECCCConfig) string {
	return c.URLOverride + "|" + c.Province + "|" + c.Site + "|" + c.Lang
}

func ecccConfigured(c ECCCConfig) bool {
	return c.URLOverride != "" || (c.Site != "" && c.Province != "")
}

// Wake makes Run check the config again now, e.g. after a settings save.
func (s *WeatherService) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Status returns the cached weather for cfg. It never does network I/O.
// A cache for another site or language counts as no data, so the frame
// never shows the old city or language while the new one loads.
func (s *WeatherService) Status(cfg ECCCConfig) WeatherStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ecccKey(cfg) != s.key {
		return WeatherStatus{}
	}
	st := WeatherStatus{Weather: s.data, FetchedAt: s.fetchedAt}
	if s.lastErr != nil {
		st.Err = s.lastErr.Error()
	}
	if s.data != nil {
		maxAge := 3 * s.ttl
		if maxAge < time.Hour {
			maxAge = time.Hour
		}
		st.Stale = s.lastErr != nil || s.Now().Sub(s.fetchedAt) > maxAge
	}
	return st
}

// due reports whether cfg needs a fetch now, and resets the cache if the
// configured location changed.
func (s *WeatherService) due(cfg ECCCConfig) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k := ecccKey(cfg); k != s.key {
		s.key, s.data, s.lastErr = k, nil, nil
		s.fetchedAt, s.attemptAt = time.Time{}, time.Time{}
	}
	s.ttl = time.Duration(cfg.RefreshSeconds) * time.Second
	if !ecccConfigured(cfg) {
		return false
	}
	if s.attemptAt.IsZero() {
		return true
	}
	wait := s.ttl
	if s.lastErr != nil {
		wait = retryAfterFail
	}
	return s.Now().Sub(s.attemptAt) >= wait
}

// Refresh fetches the citypage if the cache is due. It reports whether it
// tried. After a failure the last good data stays and is marked stale.
func (s *WeatherService) Refresh(ctx context.Context, cfg ECCCConfig) bool {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if !s.due(cfg) {
		return false
	}
	w, err := s.fetch(ctx, cfg)
	s.mu.Lock()
	defer s.mu.Unlock()
	if ecccKey(cfg) != s.key { // config changed while fetching
		return true
	}
	s.attemptAt = s.Now()
	s.lastErr = err
	if err == nil {
		s.data, s.fetchedAt = w, s.attemptAt
	}
	return true
}

// Run refreshes in the background until ctx ends, calling notify after
// every refresh attempt.
func (s *WeatherService) Run(ctx context.Context, cfg func() ECCCConfig, notify func()) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		if s.Refresh(ctx, cfg()) {
			notify()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
		}
	}
}

func (s *WeatherService) fetch(ctx context.Context, cfg ECCCConfig) (*Weather, error) {
	if cfg.URLOverride != "" {
		b, err := s.get(ctx, cfg.URLOverride)
		if err != nil {
			return nil, err
		}
		return ParseCitypage(b)
	}
	var errs []error
	u, err := s.findCitypageURL(ctx, cfg)
	if err == nil {
		b, err := s.get(ctx, u)
		if err == nil {
			w, err := ParseCitypage(b)
			if err == nil {
				return w, nil
			}
			errs = append(errs, err)
		} else {
			errs = append(errs, err)
		}
	} else {
		errs = append(errs, err)
	}
	// Legacy location, in case the dated layout is missing or broken.
	l := "e"
	if cfg.Lang == "fr" {
		l = "f"
	}
	legacy := fmt.Sprintf("%s/citypage_weather/xml/%s/%s_%s.xml", s.DD, cfg.Province, cfg.Site, l)
	b, err := s.get(ctx, legacy)
	if err == nil {
		w, err := ParseCitypage(b)
		if err == nil {
			return w, nil
		}
		errs = append(errs, err)
	} else {
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

// findCitypageURL returns the newest dated file for the site under
// today/citypage_weather/{PROV}/{HH}/, trying the current and previous 3 UTC hours.
func (s *WeatherService) findCitypageURL(ctx context.Context, cfg ECCCConfig) (string, error) {
	pat := regexp.MustCompile(`href="([^"]*_MSC_CitypageWeather_` + regexp.QuoteMeta(cfg.Site) + `_` +
		regexp.QuoteMeta(cfg.Lang) + `\.xml)"`)
	now := s.Now().UTC()
	for back := 0; back < 4; back++ {
		hh := now.Add(-time.Duration(back) * time.Hour).Format("15")
		base := fmt.Sprintf("%s/today/citypage_weather/%s/%s/", s.DD, cfg.Province, hh)
		listing, err := s.get(ctx, base)
		if err != nil {
			continue
		}
		var names []string
		for _, m := range pat.FindAllSubmatch(listing, -1) {
			names = append(names, filepath.Base(string(m[1])))
		}
		if len(names) > 0 {
			sort.Strings(names)
			return base + names[len(names)-1], nil
		}
	}
	return "", fmt.Errorf("no citypage file for %s/%s in the last 4 hours", cfg.Province, cfg.Site)
}

func (s *WeatherService) get(ctx context.Context, u string) ([]byte, error) {
	return httpGet(ctx, s.Client, u)
}

func httpGet(ctx context.Context, c *http.Client, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

var iconCodeRe = regexp.MustCompile(`^\d{2}$`)

// IconCache downloads ECCC icons once and keeps them in Dir.
type IconCache struct {
	Dir     string
	BaseURL string
	Client  *http.Client

	mu  sync.Mutex
	mem map[string][]byte
}

func NewIconCache(dir string) *IconCache {
	return &IconCache{Dir: dir, BaseURL: defaultIconURL, Client: &http.Client{Timeout: 15 * time.Second}}
}

// Get returns the GIF bytes for a two-digit icon code.
func (c *IconCache) Get(ctx context.Context, code string) ([]byte, error) {
	if !iconCodeRe.MatchString(code) {
		return nil, fmt.Errorf("bad icon code %q", code)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if b, ok := c.mem[code]; ok {
		return b, nil
	}
	path := filepath.Join(c.Dir, code+".gif")
	b, err := os.ReadFile(path)
	if err != nil {
		b, err = httpGet(ctx, c.Client, c.BaseURL+code+".gif")
		if err != nil {
			return nil, err
		}
		if err := writeBytesAtomic(path, b); err != nil {
			return nil, err
		}
	}
	if c.mem == nil {
		c.mem = map[string][]byte{}
	}
	c.mem[code] = b
	return b, nil
}

func writeBytesAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".icon-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(b)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp)
		return errors.Join(werr, cerr)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
