// honk - GUI + input (M10): an interactive demo on the framebuffer, driven by
// virtio-input. This is the kernel-side glue: it owns the policy the board
// driver and the toolkit deliberately do not - mapping raw evdev events to
// honk's UI events (keycode -> rune, tablet axes -> pixels) and pumping them
// into the toolkit, which renders to the M9 framebuffer.
//
// Input is polled (board/virt/virtioinput.go explains why a queue-backed device
// is lossless under polling); the toolkit (kernel/gui) is pure Go and host-
// tested, so this file only does translation and wiring.

//go:build tamago && riscv64

package main

import (
	"fmt"
	"image"
	"image/color"
	"time"

	"honk/board/virt"
	"honk/kernel/gui"
)

var (
	inputs *virt.Input // the input devices, nil if none
	ui     *gui.UI     // the demo UI, nil if the display or input is absent
)

// Linux evdev keycodes honk maps to runes (linux/input-event-codes.h):
// lowercase letters, digits, and space - enough to type into the demo field.
var keyRunes = map[uint16]rune{
	2: '1', 3: '2', 4: '3', 5: '4', 6: '5', 7: '6', 8: '7', 9: '8', 10: '9', 11: '0',
	16: 'q', 17: 'w', 18: 'e', 19: 'r', 20: 't', 21: 'y', 22: 'u', 23: 'i', 24: 'o', 25: 'p',
	30: 'a', 31: 's', 32: 'd', 33: 'f', 34: 'g', 35: 'h', 36: 'j', 37: 'k', 38: 'l',
	44: 'z', 45: 'x', 46: 'c', 47: 'v', 48: 'b', 49: 'n', 50: 'm', 57: ' ',
}

const (
	keyBackspace = 14
	keyEnter     = 28
	btnLeft      = 272   // BTN_LEFT
	absRange     = 32768 // QEMU virtio-tablet reports absolute axes in [0, absRange)
)

// InitUI brings up input and the interactive demo. It needs both a display (M9)
// and an input device; without either it is a no-op.
func InitUI() {
	inputs = virt.ProbeInput()
	if inputs == nil {
		fmt.Println("honk: no input device")
		return
	}
	if display == nil {
		fmt.Println("honk: input present but no display; UI disabled")
		return
	}
	buildUI()
	redrawUI()
	go inputPump()
	fmt.Printf("honk: ui up  %d input device(s); click the button and type\n", inputs.Devices())
}

func buildUI() {
	ui = gui.NewUI(display.Image().Bounds(), color.RGBA{R: 30, G: 30, B: 46, A: 255})
	field := &gui.TextField{
		Rect:     image.Rect(40, 80, 640, 124),
		OnChange: func(s string) { fmt.Printf("ui: text=%q\n", s) },
	}
	clicks := 0
	button := &gui.Button{
		Rect:    image.Rect(40, 160, 240, 220),
		Label:   "honk!",
		OnClick: func() { clicks++; fmt.Printf("ui: button clicked (clicks=%d)\n", clicks) },
	}
	ui.Add(field)
	ui.Add(button)
}

func redrawUI() {
	ui.Draw(display.Image())
	if err := display.Flush(); err != nil {
		fmt.Printf("ui: flush: %v\n", err)
	}
}

// uicmd is the shell's `ui` command: report the interactive demo's status.
func uicmd() {
	if ui == nil {
		fmt.Println("ui: not active (needs a display + an input device)")
		return
	}
	fmt.Printf("ui: active  %d input device(s)  (click the button, type into the field)\n", inputs.Devices())
}

// inputPump polls the input devices, translates evdev events into gui.Events,
// dispatches them, and repaints when the UI changes. It is the sole renderer
// after InitUI, so no drawing lock is needed.
func inputPump() {
	b := display.Image().Bounds()
	w, h := b.Dx(), b.Dy()
	var px, py int
	for {
		changed := false
		for {
			ev, ok := inputs.Read()
			if !ok {
				break
			}
			ge, send := translateInput(ev, &px, &py, w, h)
			if send && ui.Handle(ge) {
				changed = true
			}
		}
		if changed {
			redrawUI()
		}
		time.Sleep(8 * time.Millisecond)
	}
}

// translateInput maps a raw evdev event to a gui.Event, tracking the absolute
// pointer position (scaled from the tablet axis range to screen pixels). It
// returns send=false for events that produce no UI event (axis updates, key
// releases, syn frames).
func translateInput(ev virt.InputEvent, px, py *int, w, h int) (gui.Event, bool) {
	switch ev.Type {
	case virt.EvAbs:
		switch ev.Code {
		case 0: // ABS_X
			*px = int(ev.Value) * w / absRange
		case 1: // ABS_Y
			*py = int(ev.Value) * h / absRange
		}
	case virt.EvKey:
		if ev.Code == btnLeft {
			if ev.Value != 0 {
				return gui.Event{Kind: gui.PointerDown, X: *px, Y: *py}, true
			}
			return gui.Event{Kind: gui.PointerUp, X: *px, Y: *py}, true
		}
		if ev.Value == 0 { // key release: act on press only
			return gui.Event{}, false
		}
		switch ev.Code {
		case keyBackspace:
			return gui.Event{Kind: gui.KeyPress, Key: gui.KeyBackspace}, true
		case keyEnter:
			return gui.Event{Kind: gui.KeyPress, Key: gui.KeyEnter}, true
		}
		if r, ok := keyRunes[ev.Code]; ok {
			return gui.Event{Kind: gui.KeyPress, Rune: r}, true
		}
	}
	return gui.Event{}, false
}
