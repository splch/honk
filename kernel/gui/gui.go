// Package gui is honk's minimal retained-mode widget toolkit, built on the
// stdlib image/draw and a bitmap font (HONK.md §1: "2D rendering / GUI = image,
// image/draw, x/image/font; the stdlib is the graphics engine"). It is the
// pure-Go, framebuffer-agnostic half of M10: it renders into any draw.Image and
// consumes clean gui.Events, so it is host-tested (rendered pixels and all)
// while the bare-metal virtio-input/virtio-gpu drivers stay in board/virt.
//
// The model is deliberately tiny: a UI owns an ordered set of Widgets, routes
// pointer events to the widget under the cursor (and moves keyboard focus on a
// press), routes key events to the focused widget, and composites by drawing
// each widget back-to-front. Widgets report visible-state changes so the caller
// knows when to repaint.
package gui

import (
	"image"
	"image/color"
	"image/draw"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// Face is the toolkit font: the stdlib's in-memory 7x13 bitmap ASCII face, so
// honk needs no font files and text rendering is pure Go.
var Face font.Face = basicfont.Face7x13

// EventKind is the category of an input event the toolkit understands. The
// bare-metal input source (evdev over virtio-input) is translated into these by
// the caller, so the toolkit carries no device knowledge.
type EventKind int

const (
	KeyPress    EventKind = iota // a key was pressed (Rune and/or Key set)
	PointerMove                  // the pointer moved to X,Y
	PointerDown                  // the primary button went down at X,Y
	PointerUp                    // the primary button was released at X,Y
)

// Key names the non-printable keys the toolkit acts on; printable keys arrive
// as Event.Rune instead.
type Key int

const (
	KeyNone Key = iota
	KeyBackspace
	KeyEnter
)

// Event is one translated input event.
type Event struct {
	Kind EventKind
	Rune rune // for KeyPress: the printable rune, or 0 for a named Key
	Key  Key  // for KeyPress: a named key, or KeyNone
	X, Y int  // for pointer events: position in dst pixels
}

// Widget is a drawable, event-handling element. Handle reports whether the
// event changed the widget's visible state (so the UI knows to repaint).
type Widget interface {
	Bounds() image.Rectangle
	Draw(dst draw.Image)
	Handle(ev Event) bool
}

// Focusable is the optional capability of a widget that accepts keyboard focus
// (e.g. a text field). The UI grants focus on a pointer press and routes key
// events to the focused widget.
type Focusable interface {
	Widget
	SetFocused(bool)
}

// UI is a flat collection of widgets over a background, with one focused
// widget. It is not safe for concurrent use; drive it from a single goroutine
// (honk's input pump).
type UI struct {
	rect    image.Rectangle
	bg      color.RGBA
	widgets []Widget
	focus   Focusable // currently focused widget, or nil
}

// NewUI returns an empty UI covering rect with the given background color.
func NewUI(rect image.Rectangle, bg color.RGBA) *UI {
	return &UI{rect: rect, bg: bg}
}

// Add appends a widget; later widgets are on top for hit-testing and drawing.
func (u *UI) Add(w Widget) { u.widgets = append(u.widgets, w) }

// Handle dispatches one event and reports whether the UI needs repainting. Key
// events go to the focused widget; pointer events go to the widget under the
// cursor, and a pointer press also moves focus there.
func (u *UI) Handle(ev Event) bool {
	if ev.Kind == KeyPress {
		if u.focus != nil {
			return u.focus.Handle(ev)
		}
		return false
	}
	w := u.at(ev.X, ev.Y)
	changed := false
	if ev.Kind == PointerDown {
		changed = u.setFocus(w)
	}
	if w != nil && w.Handle(ev) {
		changed = true
	}
	return changed
}

// Draw paints the background and every widget back-to-front.
func (u *UI) Draw(dst draw.Image) {
	draw.Draw(dst, u.rect, &image.Uniform{C: u.bg}, image.Point{}, draw.Src)
	for _, w := range u.widgets {
		w.Draw(dst)
	}
}

// at returns the topmost widget containing (x,y), or nil.
func (u *UI) at(x, y int) Widget {
	p := image.Pt(x, y)
	for i := len(u.widgets) - 1; i >= 0; i-- {
		if p.In(u.widgets[i].Bounds()) {
			return u.widgets[i]
		}
	}
	return nil
}

// setFocus moves focus to w (if it is Focusable, else clears focus) and reports
// whether the focused widget changed.
func (u *UI) setFocus(w Widget) bool {
	f, _ := w.(Focusable)
	if f == u.focus {
		return false
	}
	if u.focus != nil {
		u.focus.SetFocused(false)
	}
	u.focus = f
	if f != nil {
		f.SetFocused(true)
	}
	return true
}

// fillRect paints a solid rectangle - the toolkit's one drawing primitive
// besides text.
func fillRect(dst draw.Image, r image.Rectangle, c color.Color) {
	draw.Draw(dst, r, &image.Uniform{C: c}, image.Point{}, draw.Src)
}

// drawBorder strokes a w-pixel border just inside r.
func drawBorder(dst draw.Image, r image.Rectangle, c color.Color, w int) {
	fillRect(dst, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+w), c)
	fillRect(dst, image.Rect(r.Min.X, r.Max.Y-w, r.Max.X, r.Max.Y), c)
	fillRect(dst, image.Rect(r.Min.X, r.Min.Y, r.Min.X+w, r.Max.Y), c)
	fillRect(dst, image.Rect(r.Max.X-w, r.Min.Y, r.Max.X, r.Max.Y), c)
}

// drawText draws s with its baseline at (x, baseline) in color c.
func drawText(dst draw.Image, x, baseline int, s string, c color.Color) {
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: Face, Dot: fixed.P(x, baseline)}
	d.DrawString(s)
}

// baseline returns the text baseline that vertically centers one line of the
// toolkit font within r.
func baselineIn(r image.Rectangle) int {
	m := Face.Metrics()
	asc, desc := m.Ascent.Round(), m.Descent.Round()
	return r.Min.Y + (r.Dy()-asc-desc)/2 + asc
}
