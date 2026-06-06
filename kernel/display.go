// honk - display/GUI (M9): a virtio-gpu framebuffer presented as a stdlib
// draw.Image (HONK.md §1: GUI is image/image/draw; the driver hides the
// virtio-gpu protocol). M9 is output-first - bring the scanout up and draw a
// test pattern - so rendering is solid before M10 adds input and a toolkit.

//go:build tamago && riscv64

package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"

	"honk/board/virt"
)

// display is the system GPU, nil if no virtio-gpu device is present.
var display *virt.GPU

// InitDisplay brings up the framebuffer and draws the test pattern. With no GPU
// it is a no-op (honk runs headless under Phase A-C just fine).
func InitDisplay() {
	display = virt.ProbeGPU()
	if display == nil {
		fmt.Println("honk: no display device")
		return
	}
	paintTestPattern(display.Image())
	if err := display.Flush(); err != nil {
		fmt.Printf("honk: display flush failed: %v\n", err)
		return
	}
	b := display.Image().Bounds()
	fmt.Printf("honk: display up  %dx%d  test pattern drawn\n", b.Dx(), b.Dy())
}

// fbcmd is the shell's `fb` command: redraw the test pattern and flush.
func fbcmd() {
	if display == nil {
		fmt.Println("fb: no display device")
		return
	}
	paintTestPattern(display.Image())
	if err := display.Flush(); err != nil {
		fmt.Printf("fb: flush: %v\n", err)
		return
	}
	b := display.Image().Bounds()
	fmt.Printf("fb: %dx%d test pattern flushed to scanout\n", b.Dx(), b.Dy())
}

// paintTestPattern fills dst with four distinct quadrants (red, green, blue,
// and near-white). The channel-distinct quadrants double as a format check: a
// screendump that reads them back in the wrong order would reveal a swapped
// pixel layout in the driver.
func paintTestPattern(dst draw.Image) {
	b := dst.Bounds()
	w, h := b.Dx(), b.Dy()
	mx, my := w/2, h/2
	fill := func(r image.Rectangle, c color.RGBA) {
		draw.Draw(dst, r, &image.Uniform{C: c}, image.Point{}, draw.Src)
	}
	fill(image.Rect(0, 0, mx, my), color.RGBA{R: 200, G: 40, B: 40, A: 255})   // top-left:  red
	fill(image.Rect(mx, 0, w, my), color.RGBA{R: 40, G: 200, B: 40, A: 255})   // top-right: green
	fill(image.Rect(0, my, mx, h), color.RGBA{R: 40, G: 40, B: 200, A: 255})   // bot-left:  blue
	fill(image.Rect(mx, my, w, h), color.RGBA{R: 220, G: 220, B: 220, A: 255}) // bot-right: white
}
