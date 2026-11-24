package main

import (
	"sync"
)

// frameHistory is how many recent frames (and their layouts) are kept, so
// a tap is matched against the frame the Kindle was actually showing.
const frameHistory = 32

type frame struct {
	version uint64
	host    string
	png     []byte
	layout  Layout
}

// frameCache renders each version once and remembers recent layouts.
type frameCache struct {
	r *Renderer

	mu     sync.Mutex
	frames []*frame // oldest first
}

// get returns the frame for version, rendering snap if needed.
func (c *frameCache) get(version uint64, snap Snapshot, host string) (*frame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, f := range c.frames {
		if f.version == version && f.host == host {
			return f, nil
		}
	}
	img, l := c.r.Render(snap, host)
	b, err := EncodePNG(img)
	if err != nil {
		return nil, err
	}
	f := &frame{version: version, host: host, png: b, layout: l}
	c.frames = append(c.frames, f)
	if len(c.frames) > frameHistory {
		c.frames = c.frames[len(c.frames)-frameHistory:]
	}
	return f, nil
}

// layout returns the layout of a frame that was served.
func (c *frameCache) layout(version uint64) (Layout, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.frames) - 1; i >= 0; i-- {
		if c.frames[i].version == version {
			return c.frames[i].layout, true
		}
	}
	return Layout{}, false
}
