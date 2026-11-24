package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite testdata/golden/*.png")

var testTime = time.Date(2026, 11, 23, 18, 5, 0, 0, time.UTC)

// testIcon is a stand-in for ECCC icons: a dark disc whose size depends
// on the code, on a transparent background.
func testIcon(code string) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, 60, 51))
	r := 12 + int(code[len(code)-1]-'0')
	for y := 0; y < 51; y++ {
		for x := 0; x < 60; x++ {
			if dx, dy := x-30, y-25; dx*dx+dy*dy <= r*r {
				img.SetNRGBA(x, y, color.NRGBA{0x50, 0x50, 0x50, 0xFF})
			}
		}
	}
	return img
}

func testWeather(t *testing.T, lang string) *Weather {
	t.Helper()
	name := "testdata/eccc/citypage_en.xml"
	if lang == LangFR {
		name = "testdata/eccc/citypage_fr.xml"
	}
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	w, err := ParseCitypage(b)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func testSnapshot(t *testing.T, lang string, rot int) Snapshot {
	s := Snapshot{
		Language: lang,
		Switches: []SwitchState{
			{ID: "light.living_room", Name: "Living Room", State: "on"},
			{ID: "light.kitchen", Name: "Kitchen", State: "off"},
			{ID: "switch.porch", Name: "Porch light", State: "off"},
			{ID: "switch.fan", Name: "Bedroom ceiling fan", State: "unavailable"},
			{ID: "scene.movie_night", Name: "Movie night", State: "scening", Action: true},
			{ID: "input_boolean.guest_mode", Name: "Guest mode", State: "on"},
		},
		Weather:        testWeather(t, lang),
		WeatherUpdated: formatClock(lang, testTime),
		Clock:          formatClock(lang, testTime),
		Date:           formatDate(lang, testTime),
		Display:        DefaultConfig().Display,
	}
	s.Display.Rotation = rot
	return s
}

func checkGolden(t *testing.T, name string, img *image.Gray) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".png")
	got, err := EncodePNG(img)
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update to create it)", err)
	}
	want, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	wg, ok := want.(*image.Gray)
	if !ok || wg.Bounds() != img.Bounds() {
		t.Fatalf("%s: golden is %T %v, frame is %v", name, want, want.Bounds(), img.Bounds())
	}
	diff := 0
	for i := range img.Pix {
		if img.Pix[i] != wg.Pix[i] {
			diff++
		}
	}
	if diff > 0 {
		out := filepath.Join(t.TempDir(), name+".png")
		os.WriteFile(out, got, 0o644)
		t.Errorf("%s: %d pixels differ from the golden image; got %s (go test -update accepts it)", name, diff, out)
	}
}

func TestGoldenFrames(t *testing.T) {
	r := NewRenderer(testIcon)
	cases := map[string]Snapshot{}
	for _, rot := range []int{0, 90, 180, 270} {
		cases[fmt.Sprintf("en_%d", rot)] = testSnapshot(t, LangEN, rot)
	}
	cases["fr_0"] = testSnapshot(t, LangFR, 0)
	cases["fr_90"] = testSnapshot(t, LangFR, 90)

	s := testSnapshot(t, LangEN, 0)
	s.HAError = "ha_unreachable"
	for i := range s.Switches {
		s.Switches[i].State = "unavailable"
	}
	s.WeatherStale = true
	s.Pending = []string{"light.kitchen"}
	s.Display.Columns = 3
	cases["en_errors"] = s

	s = testSnapshot(t, LangFR, 0)
	s.Pending = []string{"light.living_room", "scene.movie_night"}
	s.Weather = nil
	s.WeatherUpdated = ""
	s.Clock, s.Date = "", ""
	cases["fr_pending_noweather"] = s

	cases["fr_setup"] = Snapshot{Language: LangFR, Setup: true, Display: DefaultConfig().Display}

	for name, snap := range cases {
		t.Run(name, func(t *testing.T) {
			img, _ := r.Render(snap, "http://192.168.1.20:8080")
			if img.Bounds() != image.Rect(0, 0, PanelW, PanelH) {
				t.Fatalf("frame is %v", img.Bounds())
			}
			checkGolden(t, name, img)
		})
	}
}

// Rotation must be a pure turn: every canvas pixel lands on one panel
// pixel, and toCanvas undoes toPanel.
func TestRotationRoundTrip(t *testing.T) {
	for _, rot := range []int{0, 90, 180, 270} {
		w, h := canvasSize(rot)
		seen := make([]bool, PanelW*PanelH)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				px, py := toPanel(rot, x, y)
				if px < 0 || py < 0 || px >= PanelW || py >= PanelH {
					t.Fatalf("rot %d: (%d,%d) -> off panel (%d,%d)", rot, x, y, px, py)
				}
				if seen[py*PanelW+px] {
					t.Fatalf("rot %d: panel (%d,%d) hit twice", rot, px, py)
				}
				seen[py*PanelW+px] = true
				if cx, cy := toCanvas(rot, px, py); cx != x || cy != y {
					t.Fatalf("rot %d: (%d,%d) -> (%d,%d) -> (%d,%d)", rot, x, y, px, py, cx, cy)
				}
			}
		}
	}
}

// The canvas top-left corner must end up where the user, holding the
// Kindle as documented in layout.go, sees the top-left.
func TestRotationCorners(t *testing.T) {
	cases := map[int][2]int{
		0:   {PanelW - 1, 0},          // USB right: canvas top-left is the panel's top-right
		90:  {0, 0},                   // upright
		180: {0, PanelH - 1},          // USB left: panel's bottom-left
		270: {PanelW - 1, PanelH - 1}, // upside down
	}
	for rot, want := range cases {
		if px, py := toPanel(rot, 0, 0); px != want[0] || py != want[1] {
			t.Errorf("rot %d: top-left -> (%d,%d), want %v", rot, px, py, want)
		}
	}
}

// For every rotation, a tap on the panel pixel showing a tile's centre
// (and near its corners) hits that tile, and the gap between tiles is
// split between neighbours without overlap.
func TestTapMatchingAllRotations(t *testing.T) {
	r := NewRenderer(testIcon)
	for _, rot := range []int{0, 90, 180, 270} {
		for _, cols := range []int{1, 2, 3, 4} {
			s := testSnapshot(t, LangEN, rot)
			s.Display.Columns = cols
			img, l := r.Render(s, "")
			if len(l.Tiles) != len(s.Switches) {
				t.Fatalf("rot %d: %d tiles", rot, len(l.Tiles))
			}
			for _, tile := range l.Tiles {
				rc := tile.Rect
				pts := []image.Point{
					{(rc.Min.X + rc.Max.X) / 2, (rc.Min.Y + rc.Max.Y) / 2},
					{rc.Min.X + 2, rc.Min.Y + 2},
					{rc.Max.X - 3, rc.Max.Y - 3},
					{rc.Min.X - tapSlop + 1, rc.Min.Y}, // in the gap, near this tile
				}
				for _, p := range pts {
					px, py := toPanel(rot, p.X, p.Y)
					if id, ok := l.HitTest(px, py); !ok || id != tile.ID {
						t.Errorf("rot %d cols %d: tap at canvas %v (panel %d,%d) hit %q, want %q", rot, cols, p, px, py, id, tile.ID)
					}
				}
				// The tile's centre on the panel is drawn with the tile's
				// own fill, so the drawing and the hit test agree.
				px, py := toPanel(rot, rc.Min.X+3, (rc.Min.Y+rc.Max.Y)/2)
				if v := img.GrayAt(px, py).Y; v != black {
					t.Errorf("rot %d: tile %s border not black on the panel (%d)", rot, tile.ID, v)
				}
			}
			// Header and weather pane are not tiles.
			for _, p := range []image.Point{{l.W / 2, 10}, {l.Weather.Min.X + 5, l.Weather.Min.Y + 5}} {
				px, py := toPanel(rot, p.X, p.Y)
				if id, ok := l.HitTest(px, py); ok {
					t.Errorf("rot %d: tap at %v hit %q", rot, p, id)
				}
			}
		}
	}
	if _, ok := (Layout{}).HitTest(-1, 5); ok {
		t.Error("off-panel tap hit something")
	}
}

// French strings are the longest; tile states, forecast names and the
// header must never be cut. Summaries may be.
func TestFrenchTextFits(t *testing.T) {
	for _, lang := range []string{LangEN, LangFR} {
		for _, rot := range []int{0, 90} {
			for _, cols := range []int{2, 3} {
				var cut []string
				r := NewRenderer(testIcon)
				r.OnTruncate = func(kind, s string) {
					switch kind {
					case "summary", "detail":
					default:
						cut = append(cut, kind+": "+s)
					}
				}
				s := testSnapshot(t, lang, rot)
				s.Display.Columns = cols
				s.WeatherStale = true
				s.HAError = "ha_token_rejected"
				r.Render(s, "")
				s.Weather = nil
				r.Render(s, "")
				r.Render(Snapshot{Language: lang, Setup: true}, "http://192.168.100.200:18080")
				if len(cut) > 0 {
					t.Errorf("%s rot %d cols %d: text cut:\n  %s", lang, rot, cols, strings.Join(cut, "\n  "))
				}
			}
		}
	}
}

func TestRoundTemp(t *testing.T) {
	for in, want := range map[string]string{"-2.4": "-2", "-0.4": "0", "3.5": "4", "12": "12", "": "", "n/a": "n/a"} {
		if got := roundTemp(in); got != want {
			t.Errorf("roundTemp(%q) = %q, want %q", in, got, want)
		}
	}
}
