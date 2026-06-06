package gui

import (
	"image"
	"image/color"
	"testing"
)

// These tests are authoritative for the toolkit: they drive events and assert
// both the widget state transitions AND the rendered pixels in an in-memory
// image, so the bare-metal framebuffer path (board/virt) only has to deliver
// the same image to a real scanout.

func newCanvas() *image.RGBA { return image.NewRGBA(image.Rect(0, 0, 320, 240)) }

func at(img *image.RGBA, x, y int) color.RGBA { return img.RGBAAt(x, y) }

func eq(c color.RGBA, r, g, b uint8) bool { return c.R == r && c.G == g && c.B == b }

func TestButtonClickTogglesAndFires(t *testing.T) {
	clicks := 0
	btn := &Button{Rect: image.Rect(40, 40, 200, 90), Label: "honk!", OnClick: func() { clicks++ }}
	ui := NewUI(image.Rect(0, 0, 320, 240), color.RGBA{R: 30, G: 30, B: 46, A: 255})
	ui.Add(btn)

	cx, cy := 120, 65 // inside the button
	// A press alone does not fire; the release completes the click.
	ui.Handle(Event{Kind: PointerDown, X: cx, Y: cy})
	if clicks != 0 {
		t.Fatalf("press fired OnClick early (clicks=%d)", clicks)
	}
	if !ui.Handle(Event{Kind: PointerUp, X: cx, Y: cy}) {
		t.Fatal("PointerUp inside button reported no change")
	}
	if clicks != 1 || !btn.Active() {
		t.Fatalf("after click: clicks=%d active=%v, want 1 true", clicks, btn.Active())
	}

	img := newCanvas()
	ui.Draw(img)
	if c := at(img, cx, cy); !eq(c, 80, 200, 120) {
		t.Fatalf("active button center = %v, want the green active fill", c)
	}
	// The background shows where no widget is.
	if c := at(img, 5, 5); !eq(c, 30, 30, 46) {
		t.Fatalf("background = %v, want the UI background", c)
	}
}

func TestTextFieldTypingRendersAndFires(t *testing.T) {
	var last string
	tf := &TextField{Rect: image.Rect(20, 20, 300, 60), OnChange: func(s string) { last = s }}
	ui := NewUI(image.Rect(0, 0, 320, 240), color.RGBA{A: 255})
	ui.Add(tf)

	// A key with no focus is ignored.
	if ui.Handle(Event{Kind: KeyPress, Rune: 'x'}) {
		t.Fatal("key changed an unfocused UI")
	}
	if tf.Text() != "" {
		t.Fatalf("unfocused text = %q, want empty", tf.Text())
	}

	// Click to focus, then type.
	ui.Handle(Event{Kind: PointerDown, X: 40, Y: 40})
	ui.Handle(Event{Kind: PointerUp, X: 40, Y: 40})
	for _, r := range "hi" {
		ui.Handle(Event{Kind: KeyPress, Rune: r})
	}
	if tf.Text() != "hi" || last != "hi" {
		t.Fatalf("text=%q last=%q, want both \"hi\"", tf.Text(), last)
	}

	img := newCanvas()
	ui.Draw(img)
	// The focused field has the blue focus border...
	if c := at(img, tf.Rect.Min.X+10, tf.Rect.Min.Y); !eq(c, 80, 140, 255) {
		t.Fatalf("focus border = %v, want blue", c)
	}
	// ...and the typed glyphs left dark pixels on the light field.
	dark := 0
	for y := tf.Rect.Min.Y + 4; y < tf.Rect.Max.Y-4; y++ {
		for x := tf.Rect.Min.X + 4; x < tf.Rect.Max.X-4; x++ {
			c := at(img, x, y)
			if int(c.R)+int(c.G)+int(c.B) < 200 {
				dark++
			}
		}
	}
	if dark == 0 {
		t.Fatal("typed text rendered no glyph pixels")
	}

	// Backspace removes the last rune.
	ui.Handle(Event{Kind: KeyPress, Key: KeyBackspace})
	if tf.Text() != "h" {
		t.Fatalf("after backspace text=%q, want \"h\"", tf.Text())
	}
}

func TestFocusMovesBetweenFields(t *testing.T) {
	a := &TextField{Rect: image.Rect(0, 0, 100, 30)}
	b := &TextField{Rect: image.Rect(0, 40, 100, 70)}
	ui := NewUI(image.Rect(0, 0, 320, 240), color.RGBA{})
	ui.Add(a)
	ui.Add(b)

	click := func(x, y int) {
		ui.Handle(Event{Kind: PointerDown, X: x, Y: y})
		ui.Handle(Event{Kind: PointerUp, X: x, Y: y})
	}

	click(50, 15) // focus a
	ui.Handle(Event{Kind: KeyPress, Rune: 'a'})
	click(50, 55) // focus b
	ui.Handle(Event{Kind: KeyPress, Rune: 'b'})

	if a.Text() != "a" || b.Text() != "b" {
		t.Fatalf("a=%q b=%q, want a=%q b=%q", a.Text(), b.Text(), "a", "b")
	}

	// Clicking empty space (a non-focusable hit) drops focus, so keys are
	// ignored again.
	click(200, 200)
	ui.Handle(Event{Kind: KeyPress, Rune: 'z'})
	if a.Text() != "a" || b.Text() != "b" {
		t.Fatalf("key leaked after focus cleared: a=%q b=%q", a.Text(), b.Text())
	}
}
