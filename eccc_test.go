package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T) []byte {
	b, err := os.ReadFile("testdata/eccc/citypage_en.xml")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseCitypage(t *testing.T) {
	w, err := ParseCitypage(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string][2]string{
		"location":  {w.Location, "Toronto"},
		// Only the English text has the time here: "... at 18:00 UTC".
		"observed":  {w.Observed.Format(time.RFC3339), "2026-11-23T18:00:00Z"},
		"station":   {w.Station, "Toronto City"},
		"condition": {w.Condition, "Mostly Cloudy"},
		"icon":      {w.Icon, "03"},
		"temp":      {w.Temp, "-2.4"},
		"feels":     {w.Feels, "-8"},
		"humidity":  {w.Humidity, "74"},
		"pressure":  {w.Pressure, "101.7"},
		"tendency":  {w.Tendency, "rising"},
	}
	for k, v := range checks {
		if v[0] != v[1] {
			t.Errorf("%s = %q, want %q", k, v[0], v[1])
		}
	}
	if w.Wind != (Wind{Direction: "WNW", Speed: "22", Gust: "38"}) {
		t.Errorf("wind %+v", w.Wind)
	}
	if len(w.Warnings) != 1 || w.Warnings[0] != "YELLOW WARNING - WIND" {
		t.Errorf("warnings %q", w.Warnings)
	}
	if len(w.Forecast) < 6 {
		t.Fatalf("only %d forecast periods", len(w.Forecast))
	}
	p0, p1 := w.Forecast[0], w.Forecast[1]
	if p0 != (Period{Name: "Today", Icon: "03", Summary: "Mostly cloudy", POP: "30", Temp: "-1", Class: "high"}) {
		t.Errorf("period 0 %+v", p0)
	}
	if p1.Name != "Tonight" || p1.POP != "" || p1.Temp != "-8" || p1.Class != "low" {
		t.Errorf("period 1 %+v", p1)
	}
}

// citypage_fr.xml is a real file from the datamart (2026-11-24 01:01 UTC).
func TestParseCitypageFrench(t *testing.T) {
	b, err := os.ReadFile("testdata/eccc/citypage_fr.xml")
	if err != nil {
		t.Fatal(err)
	}
	w, err := ParseCitypage(b)
	if err != nil {
		t.Fatal(err)
	}
	if w.Location != "Toronto" || w.Station != "Aéroport int. Pearson de Toronto" ||
		w.Condition != "Partiellement nuageux" || w.Temp != "2.4" || w.Tendency != "à la hausse" {
		t.Errorf("current %+v", w)
	}
	// 20:00 HNE is 01:00 UTC the next day.
	if want := time.Date(2026, 11, 24, 1, 0, 0, 0, time.UTC); !w.Observed.Equal(want) || w.Observed.Location() != time.UTC {
		t.Errorf("observed %v", w.Observed)
	}
	if w.Wind != (Wind{Direction: "ONO", Speed: "22", Gust: "36"}) {
		t.Errorf("wind %+v", w.Wind)
	}
	if len(w.Forecast) != 12 || w.Forecast[0].Name != "Ce soir et cette nuit" ||
		w.Forecast[0].Temp != "-4" || w.Forecast[0].Class != "low" {
		t.Fatalf("forecast %d %+v", len(w.Forecast), w.Forecast[0])
	}
	if len(w.Warnings) != 0 {
		t.Errorf("warnings %q", w.Warnings)
	}
}

func TestParseHourly(t *testing.T) {
	b, err := os.ReadFile("testdata/eccc/citypage_fr.xml")
	if err != nil {
		t.Fatal(err)
	}
	w, err := ParseCitypage(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Hourly) != 24 {
		t.Fatalf("%d hours", len(w.Hourly))
	}
	h := w.Hourly[0]
	if got := h.Time.Format(time.RFC3339); got != "2026-11-24T02:00:00Z" {
		t.Errorf("time %s", got)
	}
	if h.Icon != "38" || h.Temp != "3" || h.POP != "40" || h.Condition != "Possibilité d'averses de neige" ||
		h.Wind != (Wind{Direction: "NO", Speed: "20", Gust: "40"}) {
		t.Errorf("hour %+v", h)
	}
	if w.Hourly[1].Time.Sub(h.Time) != time.Hour {
		t.Errorf("second hour at %v", w.Hourly[1].Time)
	}

	// The English test file has no hourly group.
	en, _ := os.ReadFile("testdata/eccc/citypage_en.xml")
	if w, err := ParseCitypage(en); err != nil || w.Hourly == nil || len(w.Hourly) != 0 {
		t.Errorf("english: %v %v", w.Hourly, err)
	}
}

func TestWeatherLocal(t *testing.T) {
	base := time.Date(2026, 11, 24, 2, 0, 0, 0, time.UTC)
	w := &Weather{Location: "x"}
	for i := 0; i < 4; i++ {
		w.Hourly = append(w.Hourly, Hour{Time: base.Add(time.Duration(i) * time.Hour)})
	}
	nst := time.FixedZone("NST", -7*3600/2) // Newfoundland, UTC-3:30
	// At 03:30 UTC the 02:00 hour is over; 03:00 is still current.
	got := w.Local(base.Add(90 * time.Minute).In(nst))
	if len(got.Hourly) != 3 || got.Location != "x" {
		t.Fatalf("got %+v", got.Hourly)
	}
	if s := got.Hourly[0].Time.Format("2006-01-02 15:04 MST"); s != "2026-11-23 23:30 NST" {
		t.Errorf("first hour %s", s)
	}
	if len(w.Hourly) != 4 || w.Hourly[1].Time.Location() != time.UTC {
		t.Error("Local changed its receiver")
	}
	if (*Weather)(nil).Local(base) != nil {
		t.Error("nil weather")
	}
}

// Feed times with only a station zone and offset, or fractional offsets.
func TestDateTimeInstant(t *testing.T) {
	want := time.Date(2026, 11, 24, 1, 30, 0, 0, time.UTC)
	for _, d := range []xDateTime{
		{Zone: "UTC", UTCOffset: "0", TimeStamp: "20261124013000"},
		{Zone: "HNT", UTCOffset: "-3.5", TimeStamp: "20261123220000"},
		{Zone: "EST", UTCOffset: "-5", Year: "2026", Month: "11", Day: "23", Hour: "20", Minute: "30"},
		{Zone: "UTC", Summary: "Tuesday November 24, 2026 at 01:30 UTC"},
	} {
		if got, ok := d.instant(); !ok || !got.Equal(want) {
			t.Errorf("%+v: %v %v", d, got, ok)
		}
	}
	// A station zone without an offset can't be placed in time.
	if _, ok := (xDateTime{Zone: "EST", Hour: "20", Minute: "00"}).instant(); ok {
		t.Error("EST without offset")
	}
}

func TestParseIgnoresUnknownElements(t *testing.T) {
	b := fixture(t)
	b = bytes.Replace(b, []byte("<currentConditions>"),
		[]byte(`<currentConditions><airQuality index="3"><new>x</new></airQuality>`), 1)
	b = bytes.Replace(b, []byte("</siteData>"), []byte(`<brandNewSection a="1"><x/></brandNewSection></siteData>`), 1)
	w, err := ParseCitypage(b)
	if err != nil {
		t.Fatal(err)
	}
	if w.Temp != "-2.4" {
		t.Fatalf("temp %q", w.Temp)
	}
}

func TestParseLatin1(t *testing.T) {
	doc := "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><siteData><location><name>Montr\xe9al</name></location>" +
		"<currentConditions><condition>Ensoleill\xe9</condition></currentConditions></siteData>"
	w, err := ParseCitypage([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if w.Location != "Montréal" || w.Condition != "Ensoleillé" {
		t.Fatalf("%q %q", w.Location, w.Condition)
	}
}

func TestParseBadXML(t *testing.T) {
	if _, err := ParseCitypage([]byte("<siteData><oops")); err == nil {
		t.Fatal("expected error")
	}
}

// fakeDD is a datamart serving the fixture in the dated layout and/or the legacy path.
type fakeDD struct {
	*httptest.Server
	mu      sync.Mutex
	hour    string // hour directory that has files
	dated   bool
	legacy  bool
	fail    bool
	body    []byte
	bodyFR  []byte
	fetched []string
}

func newFakeDD(t *testing.T) *fakeDD {
	fr, err := os.ReadFile("testdata/eccc/citypage_fr.xml")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeDD{hour: "18", dated: true, body: fixture(t), bodyFR: fr}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.fetched = append(f.fetched, r.URL.Path)
		if f.fail {
			http.Error(w, "boom", 500)
			return
		}
		dir := "/today/citypage_weather/ON/" + f.hour + "/"
		switch {
		case f.dated && r.URL.Path == dir:
			fmt.Fprintf(w, `<a href="20261123T180500.111Z_MSC_CitypageWeather_s0000458_en.xml">x</a>
<a href="20261123T183000.222Z_MSC_CitypageWeather_s0000458_en.xml">x</a>
<a href="20261123T190000.333Z_MSC_CitypageWeather_s0000458_fr.xml">x</a>
<a href="20261123T190000.333Z_MSC_CitypageWeather_s0000999_en.xml">x</a>`)
		case f.dated && r.URL.Path == dir+"20261123T183000.222Z_MSC_CitypageWeather_s0000458_en.xml":
			w.Write(f.body)
		case f.dated && r.URL.Path == dir+"20261123T190000.333Z_MSC_CitypageWeather_s0000458_fr.xml":
			w.Write(f.bodyFR)
		case f.legacy && r.URL.Path == "/citypage_weather/xml/ON/s0000458_e.xml":
			w.Write(f.body)
		case r.URL.Path == "/today/citypage_weather/docs/site_list_en.csv",
			r.URL.Path == "/today/citypage_weather/docs/site_list_fr.csv":
			b, err := os.ReadFile("testdata/eccc/" + path.Base(r.URL.Path))
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeDD) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.fetched
	f.fetched = nil
	return p
}

func (f *fakeDD) set(fn func(*fakeDD)) {
	f.mu.Lock()
	fn(f)
	f.mu.Unlock()
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newWeather(dd *fakeDD, clk *fakeClock) *WeatherService {
	w := NewWeatherService()
	w.DD, w.Now = dd.URL, clk.Now
	return w
}

var torontoCfg = ECCCConfig{Site: "s0000458", Province: "ON", Lang: "en", RefreshSeconds: 600}

func TestWeatherFindsNewestFile(t *testing.T) {
	dd := newFakeDD(t)
	dd.hour = "17" // current hour (18 UTC) is empty, so it looks back one hour
	clk := &fakeClock{t: time.Date(2026, 11, 23, 18, 20, 0, 0, time.UTC)}
	w := newWeather(dd, clk)
	if !w.Refresh(context.Background(), torontoCfg) {
		t.Fatal("first refresh should run")
	}
	st := w.Status(torontoCfg)
	if st.Weather == nil || st.Weather.Location != "Toronto" || st.Stale || st.Err != "" {
		t.Fatalf("status %+v", st)
	}
	p := dd.paths()
	if last := p[len(p)-1]; !strings.HasSuffix(last, "/17/20261123T183000.222Z_MSC_CitypageWeather_s0000458_en.xml") {
		t.Fatalf("fetched %q", p)
	}
}

func TestWeatherFrenchFeed(t *testing.T) {
	dd := newFakeDD(t)
	clk := &fakeClock{t: time.Date(2026, 11, 23, 18, 20, 0, 0, time.UTC)}
	w := newWeather(dd, clk)
	fr := torontoCfg
	fr.Lang = LangFR
	w.Refresh(context.Background(), fr)
	if st := w.Status(fr); st.Weather == nil || st.Weather.Condition != "Partiellement nuageux" {
		t.Fatalf("status %+v", st)
	}
}

func TestWeatherLegacyFallback(t *testing.T) {
	dd := newFakeDD(t)
	dd.dated, dd.legacy = false, true
	clk := &fakeClock{t: time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC)}
	w := newWeather(dd, clk)
	w.Refresh(context.Background(), torontoCfg)
	if st := w.Status(torontoCfg); st.Weather == nil || st.Err != "" {
		t.Fatalf("legacy fallback failed: %+v", st)
	}
}

func TestWeatherCacheAndStale(t *testing.T) {
	dd := newFakeDD(t)
	clk := &fakeClock{t: time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC)}
	w := newWeather(dd, clk)
	ctx := context.Background()

	w.Refresh(ctx, torontoCfg)
	dd.paths()
	clk.Add(5 * time.Minute)
	if w.Refresh(ctx, torontoCfg) || len(dd.paths()) != 0 {
		t.Fatal("refreshed inside the 10-minute cache")
	}

	// Refresh fails: keep the last good data, marked stale.
	clk.Add(6 * time.Minute)
	dd.set(func(f *fakeDD) { f.fail = true })
	if !w.Refresh(ctx, torontoCfg) {
		t.Fatal("should refresh after the TTL")
	}
	st := w.Status(torontoCfg)
	if st.Weather == nil || st.Weather.Temp != "-2.4" || !st.Stale || st.Err == "" {
		t.Fatalf("after failure: %+v", st)
	}

	// Retries after a minute, not after the full TTL.
	clk.Add(30 * time.Second)
	if w.Refresh(ctx, torontoCfg) {
		t.Fatal("retried too soon")
	}
	clk.Add(31 * time.Second)
	dd.set(func(f *fakeDD) { f.fail = false })
	if !w.Refresh(ctx, torontoCfg) {
		t.Fatal("no retry after a minute")
	}
	if st := w.Status(torontoCfg); st.Stale || st.Err != "" {
		t.Fatalf("recovered: %+v", st)
	}

	// Data that hasn't refreshed for over an hour is stale even without an error.
	clk.Add(61 * time.Minute)
	if !w.Status(torontoCfg).Stale {
		t.Fatal("old data not stale")
	}
}

func TestWeatherConfigChangeResetsCache(t *testing.T) {
	dd := newFakeDD(t)
	clk := &fakeClock{t: time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC)}
	w := newWeather(dd, clk)
	w.Refresh(context.Background(), torontoCfg)
	other := torontoCfg
	other.Site = "s0000999"
	dd.set(func(f *fakeDD) { f.fail = true })
	if !w.Refresh(context.Background(), other) {
		t.Fatal("new site should refresh at once")
	}
	if st := w.Status(other); st.Weather != nil {
		t.Fatal("old site's weather shown for the new site")
	}
	if w.Refresh(context.Background(), ECCCConfig{RefreshSeconds: 600}) {
		t.Fatal("unconfigured weather should not fetch")
	}
}

func TestIconCache(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/03.gif" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("GIF89a-fake"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	c := NewIconCache(dir)
	c.BaseURL = srv.URL + "/"
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		b, err := c.Get(ctx, "03")
		if err != nil || string(b) != "GIF89a-fake" {
			t.Fatal(b, err)
		}
	}
	if hits != 1 {
		t.Fatalf("%d downloads", hits)
	}
	// A new cache on the same dir reads the file, not the network.
	c2 := NewIconCache(dir)
	c2.BaseURL = srv.URL + "/"
	if _, err := c2.Get(ctx, "03"); err != nil || hits != 1 {
		t.Fatalf("disk cache not used: %v hits=%d", err, hits)
	}
	if _, err := c.Get(ctx, "../etc"); err == nil {
		t.Fatal("bad code accepted")
	}
	if _, err := c.Get(ctx, "99"); err == nil {
		t.Fatal("404 icon should fail")
	}
	if _, err := os.Stat(filepath.Join(dir, "99.gif")); err == nil {
		t.Fatal("failed download was cached")
	}
}
