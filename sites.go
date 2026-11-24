package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Site is one ECCC citypage location, with its name in both languages.
type Site struct {
	Code     string `json:"code"` // e.g. s0000458
	Province string `json:"province"`
	NameEN   string `json:"name_en"`
	NameFR   string `json:"name_fr"`
}

// Name returns the site name in lang.
func (s Site) Name(lang string) string {
	if lang == LangFR && s.NameFR != "" {
		return s.NameFR
	}
	return s.NameEN
}

// SiteList downloads ECCC's site lists (one CSV per language), keeps them
// for a day and saves a copy in Dir so search still works offline.
type SiteList struct {
	DD     string // datamart base URL
	Dir    string // cache directory; empty means memory only
	Client *http.Client
	Now    func() time.Time

	mu        sync.Mutex
	sites     []Site
	fetchedAt time.Time
	failedAt  time.Time
}

const sitesTTL = 24 * time.Hour

func NewSiteList(dir string) *SiteList {
	return &SiteList{DD: defaultDD, Dir: dir, Client: &http.Client{Timeout: 20 * time.Second}, Now: time.Now}
}

var siteCodeRe = regexp.MustCompile(`^s\d{7}$`)

// parseSiteCSV reads site_list_{en,fr}.csv: two header lines, then
// code, name, province, latitude, longitude.
func parseSiteCSV(b []byte) (map[string]Site, error) {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	r := csv.NewReader(bytes.NewReader(b))
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	out := map[string]Site{}
	for _, rec := range recs {
		if len(rec) < 3 || !siteCodeRe.MatchString(strings.TrimSpace(rec[0])) {
			continue // header lines
		}
		code := strings.TrimSpace(rec[0])
		out[code] = Site{Code: code, NameEN: strings.TrimSpace(rec[1]), Province: strings.TrimSpace(rec[2])}
	}
	if len(out) == 0 {
		return nil, errors.New("no sites in list")
	}
	return out, nil
}

// load returns the sites, fetching them if the cache is empty or a day old.
// When a fetch fails it falls back to the last list (memory, then disk) and
// waits a minute before trying the network again.
func (l *SiteList) load(ctx context.Context) ([]Site, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
	fresh := l.sites != nil && now.Sub(l.fetchedAt) < sitesTTL
	if fresh || (l.sites != nil && now.Sub(l.failedAt) < retryAfterFail) {
		return l.sites, nil
	}
	en, fr, err := l.fetch(ctx)
	if err == nil {
		l.sites, l.fetchedAt = mergeSites(en, fr), now
		l.save(en, fr)
		return l.sites, nil
	}
	log.Printf("eccc site list: %v", err)
	l.failedAt = now
	if l.sites == nil {
		if en, fr, derr := l.readCache(); derr == nil {
			l.sites = mergeSites(en, fr) // fetchedAt stays zero: retry after a minute
		}
	}
	if l.sites != nil {
		return l.sites, nil
	}
	return nil, err
}

func (l *SiteList) fetch(ctx context.Context) (en, fr []byte, err error) {
	base := l.DD + "/today/citypage_weather/docs/"
	if en, err = httpGet(ctx, l.Client, base+"site_list_en.csv"); err != nil {
		return nil, nil, err
	}
	if fr, err = httpGet(ctx, l.Client, base+"site_list_fr.csv"); err != nil {
		return nil, nil, err
	}
	if _, err := parseSiteCSV(en); err != nil {
		return nil, nil, fmt.Errorf("site_list_en.csv: %w", err)
	}
	return en, fr, nil
}

func (l *SiteList) save(en, fr []byte) {
	if l.Dir == "" {
		return
	}
	for name, b := range map[string][]byte{"site_list_en.csv": en, "site_list_fr.csv": fr} {
		if err := writeBytesAtomic(filepath.Join(l.Dir, name), b); err != nil {
			log.Printf("eccc site list: save %s: %v", name, err)
		}
	}
}

func (l *SiteList) readCache() (en, fr []byte, err error) {
	if l.Dir == "" {
		return nil, nil, os.ErrNotExist
	}
	if en, err = os.ReadFile(filepath.Join(l.Dir, "site_list_en.csv")); err != nil {
		return nil, nil, err
	}
	fr, _ = os.ReadFile(filepath.Join(l.Dir, "site_list_fr.csv"))
	return en, fr, nil
}

// mergeSites joins the two lists by code. The French list only adds names.
func mergeSites(en, fr []byte) []Site {
	sites, _ := parseSiteCSV(en)
	frSites, _ := parseSiteCSV(fr)
	out := make([]Site, 0, len(sites))
	for code, s := range sites {
		s.NameFR = frSites[code].NameEN // parseSiteCSV puts column 2 in NameEN
		if s.NameFR == "" {
			s.NameFR = s.NameEN
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// fold lowercases s and strips accents, so "montreal" finds "Montréal".
func fold(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// Search finds sites whose name (in either language) contains q, ignoring
// case and accents, optionally in one province. Names that start with q
// come first, then the rest alphabetically in lang. At most limit results.
func (l *SiteList) Search(ctx context.Context, q, province, lang string, limit int) ([]Site, error) {
	sites, err := l.load(ctx)
	if err != nil {
		return nil, err
	}
	q = fold(strings.TrimSpace(q))
	province = strings.ToUpper(strings.TrimSpace(province))
	type hit struct {
		s      Site
		prefix bool
		key    string
	}
	var hits []hit
	for _, s := range sites {
		if province != "" && s.Province != province {
			continue
		}
		en, fr := fold(s.NameEN), fold(s.NameFR)
		if q != "" && !strings.Contains(en, q) && !strings.Contains(fr, q) {
			continue
		}
		hits = append(hits, hit{s, q != "" && (strings.HasPrefix(en, q) || strings.HasPrefix(fr, q)), fold(s.Name(lang))})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].prefix != hits[j].prefix {
			return hits[i].prefix
		}
		return hits[i].key < hits[j].key
	})
	out := []Site{}
	for i := 0; i < len(hits) && i < limit; i++ {
		out = append(out, hits[i].s)
	}
	return out, nil
}

// Lookup returns the site with code, if it is in the list.
func (l *SiteList) Lookup(ctx context.Context, code string) (Site, bool, error) {
	sites, err := l.load(ctx)
	if err != nil {
		return Site{}, false, err
	}
	for _, s := range sites {
		if s.Code == code {
			return s, true, nil
		}
	}
	return Site{}, false, nil
}
