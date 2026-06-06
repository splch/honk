package gui

import (
	"image"
	"image/color"
	"image/draw"
)

// Button is a clickable, toggling button. A full press-then-release inside it
// fires OnClick and flips its highlighted state, which gives it a distinct fill
// color (so a screendump can confirm the click landed).
type Button struct {
	Rect    image.Rectangle
	Label   string
	OnClick func()

	pressed bool // primary button is down inside this button
	active  bool // toggled on by a completed click
}

func (b *Button) Bounds() image.Rectangle { return b.Rect }

// Active reports the toggled state (exported for tests and callers).
func (b *Button) Active() bool { return b.active }

func (b *Button) Handle(ev Event) bool {
	switch ev.Kind {
	case PointerDown:
		b.pressed = true
		return true
	case PointerUp:
		if b.pressed {
			b.pressed = false
			b.active = !b.active
			if b.OnClick != nil {
				b.OnClick()
			}
			return true
		}
	}
	return false
}

func (b *Button) Draw(dst draw.Image) {
	bg := color.RGBA{R: 90, G: 90, B: 110, A: 255}
	switch {
	case b.pressed:
		bg = color.RGBA{R: 60, G: 60, B: 80, A: 255}
	case b.active:
		bg = color.RGBA{R: 80, G: 200, B: 120, A: 255} // distinct "on" fill
	}
	fillRect(dst, b.Rect, bg)
	drawText(dst, b.Rect.Min.X+10, baselineIn(b.Rect), b.Label, color.White)
}

// TextField is a single-line editable text box. It accepts typed runes and
// backspace while focused, and fires OnChange after each edit.
type TextField struct {
	Rect     image.Rectangle
	OnChange func(string)

	text    []rune
	focused bool
}

func (t *TextField) Bounds() image.Rectangle { return t.Rect }
func (t *TextField) SetFocused(f bool)       { t.focused = f }

// Text returns the current contents.
func (t *TextField) Text() string { return string(t.text) }

func (t *TextField) Handle(ev Event) bool {
	if ev.Kind != KeyPress {
		return false
	}
	switch {
	case ev.Key == KeyBackspace:
		if len(t.text) == 0 {
			return false
		}
		t.text = t.text[:len(t.text)-1]
	case ev.Rune != 0:
		t.text = append(t.text, ev.Rune)
	default:
		return false
	}
	if t.OnChange != nil {
		t.OnChange(string(t.text))
	}
	return true
}

func (t *TextField) Draw(dst draw.Image) {
	fillRect(dst, t.Rect, color.RGBA{R: 240, G: 240, B: 240, A: 255})
	if t.focused {
		drawBorder(dst, t.Rect, color.RGBA{R: 80, G: 140, B: 255, A: 255}, 2)
	}
	drawText(dst, t.Rect.Min.X+6, baselineIn(t.Rect), t.Text(), color.RGBA{A: 255})
}
