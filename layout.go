package main

import (
	"image"
	"math"
)

// The panel as FBInk and the touchscreen see it: portrait, upright with the
// USB port at the bottom. Touch coordinates are panel pixels (Stage 0).
const (
	PanelW = 1072
	PanelH = 1448
)

// Rotation is how the Kindle is held, in degrees clockwise from the default
// landscape position:
//
//	  0: landscape, USB port on the right (the default)
//	 90: portrait, USB port at the bottom (the Kindle's normal way up)
//	180: landscape, USB port on the left
//	270: portrait, upside down
//
// The frame is drawn on a canvas the size the user sees (1448×1072 in
// landscape, 1072×1448 in portrait) and then turned into the panel frame.

// canvasSize gives the canvas width and height for a rotation.
func canvasSize(rot int) (w, h int) {
	if rot == 90 || rot == 270 {
		return PanelW, PanelH
	}
	return PanelH, PanelW
}

// toPanel maps a canvas pixel to the panel pixel that shows it.
func toPanel(rot, x, y int) (px, py int) {
	switch rot {
	case 0:
		return PanelW - 1 - y, x
	case 180:
		return y, PanelH - 1 - x
	case 270:
		return PanelW - 1 - x, PanelH - 1 - y
	default:
		return x, y
	}
}

// toCanvas is the inverse of toPanel, used for taps.
func toCanvas(rot, px, py int) (x, y int) {
	switch rot {
	case 0:
		return py, PanelW - 1 - px
	case 180:
		return PanelH - 1 - py, px
	case 270:
		return PanelW - 1 - px, PanelH - 1 - py
	default:
		return px, py
	}
}

// Layout sizes. Hit targets stay well above the ~110 px (9 mm) minimum
// unless there are too many tiles for the pane.
const (
	margin     = 24
	headerH    = 96
	tileGap    = 20
	maxTileH   = 240
	bannerH    = 64
	tapSlop    = tileGap / 2 // a tap in the gap counts for the nearer tile
	paneDivide = 3           // divider line width
)

// scaled multiplies a size by a section scale. At 1 it returns v exactly,
// so the normal layout is unchanged.
func scaled(v int, s float64) int {
	if s <= 0 || s == 1 {
		return v
	}
	return int(math.Round(float64(v) * s))
}

// Tile is one switch's rectangle on the canvas.
type Tile struct {
	ID   string
	Rect image.Rectangle
}

// Layout is where everything goes on the canvas. Drawing and tap matching
// both use it, so a tap always matches what was drawn.
type Layout struct {
	Rotation int
	W, H     int
	Header   image.Rectangle
	Switches image.Rectangle // left (landscape) or top (portrait) pane
	Weather  image.Rectangle
	Banner   image.Rectangle // HA error banner inside the switches pane; empty if none
	Divider  image.Rectangle
	Tiles    []Tile
}

// ComputeLayout lays out a snapshot.
func ComputeLayout(s Snapshot) Layout {
	rot := s.Display.Rotation
	w, h := canvasSize(rot)
	l := Layout{Rotation: rot, W: w, H: h}
	if s.Setup {
		return l
	}
	sc := s.Display.Scale.Norm()
	hh := scaled(headerH, sc.Header)
	l.Header = image.Rect(0, 0, w, hh)
	body := image.Rect(margin, hh+margin, w-margin, h-margin)
	split := s.Display.Split
	if split <= 0 {
		split = 0.55
	}
	if w > h { // landscape: switches left, weather right
		x := body.Min.X + int(float64(body.Dx())*split)
		l.Switches = image.Rect(body.Min.X, body.Min.Y, x-margin, body.Max.Y)
		l.Divider = image.Rect(x-paneDivide/2, body.Min.Y, x-paneDivide/2+paneDivide, body.Max.Y)
		l.Weather = image.Rect(x+margin, body.Min.Y, body.Max.X, body.Max.Y)
	} else { // portrait: switches on top, weather below
		y := body.Min.Y + int(float64(body.Dy())*split)
		l.Switches = image.Rect(body.Min.X, body.Min.Y, body.Max.X, y-margin)
		l.Divider = image.Rect(body.Min.X, y-paneDivide/2, body.Max.X, y-paneDivide/2+paneDivide)
		l.Weather = image.Rect(body.Min.X, y+margin, body.Max.X, body.Max.Y)
	}

	grid := l.Switches
	if s.HAError != "" {
		bh := scaled(bannerH, sc.Switches)
		l.Banner = image.Rect(grid.Min.X, grid.Min.Y, grid.Max.X, grid.Min.Y+bh)
		grid.Min.Y += bh + tileGap
	}
	n := len(s.Switches)
	if n == 0 {
		return l
	}
	cols := s.Display.Columns
	if cols < 1 {
		cols = 1
	}
	if cols > n {
		cols = n
	}
	rows := (n + cols - 1) / cols
	tw := (grid.Dx() - (cols-1)*tileGap) / cols
	th := (grid.Dy() - (rows-1)*tileGap) / rows
	th = min(th, scaled(maxTileH, sc.Switches))
	for i, sw := range s.Switches {
		r, c := i/cols, i%cols
		x := grid.Min.X + c*(tw+tileGap)
		y := grid.Min.Y + r*(th+tileGap)
		l.Tiles = append(l.Tiles, Tile{ID: sw.ID, Rect: image.Rect(x, y, x+tw, y+th)})
	}
	return l
}

// HitTest finds the tile under a tap at panel coordinates.
func (l Layout) HitTest(px, py int) (string, bool) {
	if px < 0 || py < 0 || px >= PanelW || py >= PanelH {
		return "", false
	}
	x, y := toCanvas(l.Rotation, px, py)
	p := image.Pt(x, y)
	for _, t := range l.Tiles {
		if p.In(t.Rect.Inset(-tapSlop)) {
			return t.ID, true
		}
	}
	return "", false
}
