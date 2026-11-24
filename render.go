package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/png"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// E-ink palette: black, white and one light gray for unavailable tiles.
// Text is always black or white.
const (
	black  = 0x00
	white  = 0xFF
	grayNA = 0xC8
)

// Renderer draws snapshots into panel frames. It is safe for concurrent
// use; frames are drawn one at a time.
type Renderer struct {
	// Icons returns the ECCC icon for a code, or nil. May be nil.
	Icons func(code string) image.Image
	// OnTruncate, if set, is called whenever text had to be cut to fit.
	// kind says which element it was (tile_name, tile_state, period, ...).
	OnTruncate func(kind, text string)

	mu            sync.Mutex
	regular, bold *opentype.Font
	faces         map[faceKey]font.Face
}

type faceKey struct {
	bold bool
	size int
}

func NewRenderer(icons func(string) image.Image) *Renderer {
	r := &Renderer{Icons: icons, faces: map[faceKey]font.Face{}}
	var err error
	if r.regular, err = opentype.Parse(goregular.TTF); err != nil {
		panic(err)
	}
	if r.bold, err = opentype.Parse(gobold.TTF); err != nil {
		panic(err)
	}
	return r
}

func (r *Renderer) face(bold bool, size int) font.Face {
	k := faceKey{bold, size}
	if f, ok := r.faces[k]; ok {
		return f
	}
	src := r.regular
	if bold {
		src = r.bold
	}
	f, err := opentype.NewFace(src, &opentype.FaceOptions{Size: float64(size), DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		panic(err)
	}
	r.faces[k] = f
	return f
}

// Render draws s and returns the panel frame (1072×1448, already rotated)
// and the layout used. host (e.g. "http://192.168.1.5:8080") is shown on
// the setup frame.
func (r *Renderer) Render(s Snapshot, host string) (*image.Gray, Layout) {
	r.mu.Lock()
	defer r.mu.Unlock()
	l := ComputeLayout(s)
	c := &canvas{r: r, img: image.NewGray(image.Rect(0, 0, l.W, l.H)), lang: s.Language}
	c.fill(c.img.Bounds(), white)
	if s.Setup {
		c.drawSetup(host)
	} else {
		c.drawHeader(s, l.Header)
		c.drawSwitches(s, l)
		c.fill(l.Divider, black)
		c.drawWeather(s.Weather, l.Weather)
	}
	return rotateToPanel(c.img, l.Rotation), l
}

// rotateToPanel turns a canvas into the panel frame.
func rotateToPanel(src *image.Gray, rot int) *image.Gray {
	dst := image.NewGray(image.Rect(0, 0, PanelW, PanelH))
	b := src.Bounds()
	for y := 0; y < b.Dy(); y++ {
		row := src.Pix[y*src.Stride : y*src.Stride+b.Dx()]
		for x, v := range row {
			px, py := toPanel(rot, x, y)
			dst.Pix[py*dst.Stride+px] = v
		}
	}
	return dst
}

// EncodePNG encodes an 8-bit grayscale PNG.
func EncodePNG(img *image.Gray) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// canvas is one frame being drawn.
type canvas struct {
	r    *Renderer
	img  *image.Gray
	lang string
}

func (c *canvas) face(bold bool, size int) font.Face { return c.r.face(bold, size) }

func (c *canvas) fill(rect image.Rectangle, v uint8) {
	draw.Draw(c.img, rect, image.NewUniform(color.Gray{v}), image.Point{}, draw.Src)
}

// stroke draws a rectangle outline of thickness t inside rect.
func (c *canvas) stroke(rect image.Rectangle, t int, v uint8) {
	c.fill(image.Rect(rect.Min.X, rect.Min.Y, rect.Max.X, rect.Min.Y+t), v)
	c.fill(image.Rect(rect.Min.X, rect.Max.Y-t, rect.Max.X, rect.Max.Y), v)
	c.fill(image.Rect(rect.Min.X, rect.Min.Y, rect.Min.X+t, rect.Max.Y), v)
	c.fill(image.Rect(rect.Max.X-t, rect.Min.Y, rect.Max.X, rect.Max.Y), v)
}

// dashed draws a dashed outline of thickness t inside rect.
func (c *canvas) dashed(rect image.Rectangle, t, on, off int, v uint8) {
	for x := rect.Min.X; x < rect.Max.X; x += on + off {
		x2 := min(x+on, rect.Max.X)
		c.fill(image.Rect(x, rect.Min.Y, x2, rect.Min.Y+t), v)
		c.fill(image.Rect(x, rect.Max.Y-t, x2, rect.Max.Y), v)
	}
	for y := rect.Min.Y; y < rect.Max.Y; y += on + off {
		y2 := min(y+on, rect.Max.Y)
		c.fill(image.Rect(rect.Min.X, y, rect.Min.X+t, y2), v)
		c.fill(image.Rect(rect.Max.X-t, y, rect.Max.X, y2), v)
	}
}

func ascent(f font.Face) int  { return f.Metrics().Ascent.Ceil() }
func descent(f font.Face) int { return f.Metrics().Descent.Ceil() }
func lineH(f font.Face) int   { return f.Metrics().Height.Ceil() }

func width(f font.Face, s string) int { return font.MeasureString(f, s).Ceil() }

// text draws s with its baseline at y and returns its width.
func (c *canvas) text(f font.Face, x, y int, s string, v uint8) int {
	d := font.Drawer{Dst: c.img, Src: image.NewUniform(color.Gray{v}), Face: f, Dot: fixed.P(x, y)}
	d.DrawString(s)
	return (d.Dot.X - fixed.I(x)).Ceil()
}

// textMid draws s vertically centred on mid.
func (c *canvas) textMid(f font.Face, x, mid int, s string, v uint8) int {
	return c.text(f, x, mid+(ascent(f)-descent(f))/2, s, v)
}

// textCentered draws s centred horizontally in [x0, x1).
func (c *canvas) textCentered(f font.Face, x0, x1, y int, s string, v uint8) {
	c.text(f, x0+(x1-x0-width(f, s))/2, y, s, v)
}

const ellipsis = "…"

// truncate cuts s to fit w, adding an ellipsis, and reports the cut.
func (c *canvas) truncate(f font.Face, s string, w int, kind string) string {
	if width(f, s) <= w {
		return s
	}
	if c.r.OnTruncate != nil {
		c.r.OnTruncate(kind, s)
	}
	rs := []rune(s)
	for n := len(rs) - 1; n > 0; n-- {
		t := strings.TrimRight(string(rs[:n]), " ,.") + ellipsis
		if width(f, t) <= w {
			return t
		}
	}
	return ellipsis
}

// fit picks the largest size from hi down to lo at which s fits w, and
// truncates s at lo if it never fits.
func (c *canvas) fit(bold bool, hi, lo int, s string, w int, kind string) (font.Face, string) {
	for size := hi; size > lo; size -= 2 {
		if f := c.face(bold, size); width(f, s) <= w {
			return f, s
		}
	}
	f := c.face(bold, lo)
	return f, c.truncate(f, s, w, kind)
}

// wrapWords breaks s into lines no wider than w where possible. A single
// word wider than w gets a line of its own.
func wrapWords(f font.Face, s string, w int) []string {
	var lines []string
	cur := ""
	for _, word := range strings.Fields(s) {
		if cur == "" {
			cur = word
		} else if width(f, cur+" "+word) <= w {
			cur += " " + word
		} else {
			lines = append(lines, cur)
			cur = word
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// fitWrapped picks the largest size at which s wraps into at most maxLines
// lines of width w and height maxH. At lo it truncates what doesn't fit.
func (c *canvas) fitWrapped(bold bool, hi, lo int, s string, w, maxLines, maxH int, kind string) (font.Face, []string) {
	for size := hi; size >= lo; size -= 2 {
		f := c.face(bold, size)
		lines := wrapWords(f, s, w)
		n := len(lines)
		if n > maxLines || n*lineH(f) > maxH && n > 1 {
			continue
		}
		ok := true
		for _, ln := range lines {
			ok = ok && width(f, ln) <= w
		}
		if ok {
			return f, lines
		}
	}
	f := c.face(bold, lo)
	lines := wrapWords(f, s, w)
	if fitLines := max(1, maxH/lineH(f)); maxLines > fitLines {
		maxLines = fitLines
	}
	if len(lines) > maxLines {
		// The joined tail is always wider than w, so truncate cuts it.
		rest := strings.Join(lines[maxLines-1:], " ")
		lines = append(lines[:maxLines-1], rest)
	}
	for i := range lines {
		lines[i] = c.truncate(f, lines[i], w, kind)
	}
	return f, lines
}

func (c *canvas) drawSetup(host string) {
	b := c.img.Bounds()
	w := b.Dx() - 4*margin
	f, title := c.fit(true, 72, 36, T(c.lang, "setup_title"), w, "setup")
	c.textCentered(f, 0, b.Dx(), b.Dy()/2-40, title, black)
	if host == "" {
		host = "http://<server>"
	}
	hf, lines := c.fitWrapped(false, 40, 26, T(c.lang, "setup_hint", host), w, 3, 3*60, "setup")
	y := b.Dy()/2 + 40 + ascent(hf)
	for _, ln := range lines {
		c.textCentered(hf, 0, b.Dx(), y, ln, black)
		y += lineH(hf)
	}
}

func (c *canvas) drawHeader(s Snapshot, r image.Rectangle) {
	mid := r.Min.Y + r.Dy()/2
	x := r.Min.X + margin
	if s.Clock != "" {
		x += c.textMid(c.face(true, 60), x, mid, s.Clock, black) + 28
		if s.Date != "" {
			c.textMid(c.face(false, 36), x, mid, s.Date, black)
		}
	}
	right := r.Max.X - margin
	if s.WeatherStale {
		f := c.face(true, 30)
		old := T(c.lang, "weather_old")
		box := image.Rect(right-width(f, old)-24, mid-24, right, mid+24)
		c.fill(box, black)
		c.textMid(f, box.Min.X+12, mid, old, white)
		right = box.Min.X - 16
	}
	if s.WeatherUpdated != "" {
		f := c.face(false, 28)
		t := T(c.lang, "weather_updated", s.WeatherUpdated)
		c.textMid(f, right-width(f, t), mid, t, black)
	}
	c.fill(image.Rect(r.Min.X, r.Max.Y-3, r.Max.X, r.Max.Y), black)
}

func (c *canvas) drawSwitches(s Snapshot, l Layout) {
	if !l.Banner.Empty() {
		b := l.Banner
		c.fill(b, black)
		f, t := c.fit(true, 32, 22, T(c.lang, s.HAError), b.Dx()-40, "banner")
		c.textMid(f, b.Min.X+20, b.Min.Y+b.Dy()/2, t, white)
	}
	if len(s.Switches) == 0 {
		p := l.Switches
		f, t := c.fit(true, 36, 24, T(c.lang, "no_switches"), p.Dx(), "no_switches")
		c.textCentered(f, p.Min.X, p.Max.X, p.Min.Y+p.Dy()/2, t, black)
		return
	}
	pending := map[string]bool{}
	for _, id := range s.Pending {
		pending[id] = true
	}
	for i, t := range l.Tiles {
		c.drawTile(s.Switches[i], t.Rect, pending[t.ID])
	}
}

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }

func (c *canvas) drawTile(sw SwitchState, r image.Rectangle, pending bool) {
	var bg, fg uint8 = white, black
	state := T(c.lang, "tile_off")
	pill := false
	switch {
	case !sw.Available():
		bg, state = grayNA, T(c.lang, "tile_na")
	case sw.Action:
		state, pill = T(c.lang, "tile_run"), true
	case sw.On():
		bg, fg, state = black, white, T(c.lang, "tile_on")
	}
	c.fill(r, bg)
	if bg != black {
		c.stroke(r, 5, black)
	}
	pad := clamp(r.Dy()/10, 12, 22)
	inner := r.Inset(pad)
	pillPad := 0
	if pill {
		pillPad = 14
	}
	sf, st := c.fit(true, clamp(r.Dy()*22/100, 20, 44), 18, state, inner.Dx()-2*pillPad, "tile_state")
	base := inner.Max.Y - descent(sf)
	if pill {
		box := image.Rect(inner.Min.X, base-ascent(sf)-6, inner.Min.X+width(sf, st)+2*pillPad, inner.Max.Y+4)
		c.fill(box, black)
		c.text(sf, box.Min.X+pillPad, base, st, white)
	} else {
		c.text(sf, inner.Min.X, base, st, fg)
	}
	area := image.Rect(inner.Min.X, inner.Min.Y, inner.Max.X, base-ascent(sf)-10)
	nf, lines := c.fitWrapped(true, clamp(r.Dy()*24/100, 20, 48), 18, sw.Name, area.Dx(), 2, area.Dy(), "tile_name")
	y := area.Min.Y + ascent(nf)
	for _, ln := range lines {
		c.text(nf, area.Min.X, y, ln, fg)
		y += lineH(nf)
	}
	if pending {
		c.dashed(r.Inset(-9), 5, 16, 10, black)
	}
}

// roundTemp turns "-2.4" into "-2". Anything unparseable is kept as is.
func roundTemp(s string) string {
	f, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64)
	if err != nil {
		return s
	}
	n := int(math.Round(f))
	return strconv.Itoa(n) // never "-0": Itoa(0) is "0"
}

func (c *canvas) drawWeather(w *Weather, r image.Rectangle) {
	if w == nil {
		f := c.face(true, 36)
		c.textCentered(f, r.Min.X, r.Max.X, r.Min.Y+r.Dy()/2, T(c.lang, "weather_none"), black)
		return
	}
	y := r.Min.Y
	f, t := c.fit(true, 40, 28, w.Location, r.Dx(), "location")
	y += ascent(f)
	c.text(f, r.Min.X, y, t, black)
	y += descent(f) + 14

	for i, warn := range w.Warnings {
		if i == 2 {
			break
		}
		box := image.Rect(r.Min.X, y, r.Max.X, y+52)
		c.fill(box, black)
		wf, wt := c.fit(true, 28, 20, warn, box.Dx()-32, "warning")
		c.textMid(wf, box.Min.X+16, box.Min.Y+26, wt, white)
		y += 52 + 12
	}

	// Current conditions: icon and big temperature, then the details,
	// beside them when the pane is wide (portrait) and below otherwise.
	const curH = 150
	c.icon(w.Icon, image.Rect(r.Min.X, y, r.Min.X+170, y+curH))
	tx := r.Min.X + 190
	if w.Temp != "" {
		tf := c.face(true, 120)
		tx += c.text(tf, tx, y+curH/2+(ascent(tf)-descent(tf))/2-6, roundTemp(w.Temp)+"°", black) + 32
	}
	var feels, hp []string
	if w.Feels != "" {
		feels = append(feels, T(c.lang, "feels_like", roundTemp(w.Feels)))
	}
	if wt := WindText(c.lang, w.Wind); wt != "" {
		feels = append(feels, T(c.lang, "wind", wt))
	}
	if w.Humidity != "" {
		hp = append(hp, T(c.lang, "humidity", w.Humidity))
	}
	if w.Pressure != "" {
		hp = append(hp, T(c.lang, "pressure", w.Pressure))
	}
	const sep = "  ·  "
	// Wide pane (portrait): details beside the temperature, one per line.
	// Narrow pane (landscape): below it, paired up.
	dx, dw, dy := r.Min.X, r.Dx(), y+curH+8
	details := []string{strings.Join(feels, sep), strings.Join(hp, sep)}
	if r.Max.X-tx >= 420 {
		dx, dw, dy = tx, r.Max.X-tx, y+4
		details = append(feels, details[1])
	}
	if w.Condition != "" {
		cf, ct := c.fit(true, 34, 24, w.Condition, dw, "condition")
		dy += ascent(cf)
		c.text(cf, dx, dy, ct, black)
		dy += descent(cf) + 8
	}
	for _, d := range details {
		if d == "" {
			continue
		}
		df, dt := c.fit(false, 28, 20, d, dw, "detail")
		dy += ascent(df)
		c.text(df, dx, dy, dt, black)
		dy += descent(df) + 6
	}
	if w.Observed != "" {
		of, ot := c.fit(false, 24, 18, T(c.lang, "observed_at", localTime(c.lang, w.Observed)), dw, "observed")
		dy += ascent(of)
		c.text(of, dx, dy, ot, black)
		dy += descent(of)
	}
	y = max(dy, y+curH) + 14

	// Forecast rows fill the rest.
	c.fill(image.Rect(r.Min.X, y, r.Max.X, y+3), black)
	y += 3 + 6
	avail := r.Max.Y - y
	const minRow = 84
	n := min(6, len(w.Forecast), avail/minRow)
	if n <= 0 {
		return
	}
	rowH := min(avail/n, 130)
	for i := 0; i < n; i++ {
		c.drawPeriod(w.Forecast[i], image.Rect(r.Min.X, y, r.Max.X, y+rowH))
		y += rowH
	}
}

func (c *canvas) drawPeriod(p Period, r image.Rectangle) {
	ic := min(r.Dy()-12, 90)
	c.icon(p.Icon, image.Rect(r.Min.X, r.Min.Y+(r.Dy()-ic)/2, r.Min.X+ic, r.Min.Y+(r.Dy()+ic)/2))
	x := r.Min.X + ic + 16
	size := clamp(r.Dy()*32/100, 22, 32)
	b1 := r.Min.Y + r.Dy()*44/100
	b2 := r.Min.Y + r.Dy()*84/100

	right := r.Max.X
	if p.Temp != "" {
		key := "temp_high"
		if p.Class == "low" {
			key = "temp_low"
		}
		tf := c.face(true, size)
		t := T(c.lang, key, roundTemp(p.Temp))
		right -= width(tf, t)
		c.text(tf, right, b1, t, black)
		right -= 20
	}
	if p.POP != "" {
		pf := c.face(false, size)
		t := T(c.lang, "pop", p.POP)
		right -= width(pf, t)
		c.text(pf, right, b1, t, black)
		right -= 20
	}
	nf, name := c.fit(true, size, 20, p.Name, right-x, "period")
	c.text(nf, x, b1, name, black)
	sf, sum := c.fit(false, clamp(r.Dy()*28/100, 20, 28), 20, p.Summary, r.Max.X-x, "summary")
	c.text(sf, x, b2, sum, black)
}

// icon draws an ECCC icon scaled to fit box, keeping its aspect ratio.
func (c *canvas) icon(code string, box image.Rectangle) {
	if code == "" || c.r.Icons == nil {
		return
	}
	src := c.r.Icons(code)
	if src == nil {
		return
	}
	sb := src.Bounds()
	if sb.Empty() {
		return
	}
	scale := math.Min(float64(box.Dx())/float64(sb.Dx()), float64(box.Dy())/float64(sb.Dy()))
	dw, dh := int(float64(sb.Dx())*scale), int(float64(sb.Dy())*scale)
	x, y := box.Min.X+(box.Dx()-dw)/2, box.Min.Y+(box.Dy()-dh)/2
	xdraw.CatmullRom.Scale(c.img, image.Rect(x, y, x+dw, y+dh), src, sb, xdraw.Over, nil)
}

// IconImages decodes ECCC icons from an IconCache and prepares them for
// e-ink. Failures are remembered for a while so a missing icon doesn't
// slow down every frame.
type IconImages struct {
	Cache   *IconCache
	Timeout time.Duration

	mu     sync.Mutex
	imgs   map[string]image.Image
	failed map[string]time.Time
}

func NewIconImages(c *IconCache) *IconImages {
	return &IconImages{Cache: c, Timeout: 4 * time.Second, imgs: map[string]image.Image{}, failed: map[string]time.Time{}}
}

func (ii *IconImages) Get(code string) image.Image {
	ii.mu.Lock()
	defer ii.mu.Unlock()
	if img, ok := ii.imgs[code]; ok {
		return img
	}
	if t, ok := ii.failed[code]; ok && time.Since(t) < 5*time.Minute {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), ii.Timeout)
	defer cancel()
	b, err := ii.Cache.Get(ctx, code)
	var src image.Image
	if err == nil {
		src, err = gif.Decode(bytes.NewReader(b))
	}
	if err != nil {
		log.Printf("weather icon %s: %v", code, err)
		ii.failed[code] = time.Now()
		return nil
	}
	img := einkIcon(src)
	ii.imgs[code] = img
	return img
}

// einkIcon converts a colour icon to gray, keeping transparency. ECCC
// icons are pale (yellow sun, light blue cloud), so it darkens them to
// stay visible on the panel.
func einkIcon(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			c := color.NRGBAModel.Convert(src.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			l := (299*float64(c.R) + 587*float64(c.G) + 114*float64(c.B)) / 1000 / 255
			v := uint8(math.Round(255 * l * l))
			dst.SetNRGBA(x, y, color.NRGBA{v, v, v, c.A})
		}
	}
	return dst
}
