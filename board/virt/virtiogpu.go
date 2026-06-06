// honk - QEMU virt board: virtio-gpu driver (M9 framebuffer).
//
// A focused 2D driver over the shared virtio-mmio v2 transport (virtio.go). It
// hides the virtio-gpu control protocol entirely and presents the scanout as a
// stdlib draw.Image: callers draw into Image() with image/draw and call Flush()
// to push the pixels to the host display (HONK.md §1: "the stdlib is the
// graphics engine"). The wire format - the control commands, the single
// scanout resource, and the pixel layout - lives only here.
//
// Each control command is a 2-descriptor chain (request device-readable, reply
// device-writable) published and polled to completion, exactly like honk's
// virtio-9p path. honk is identity-mapped (satp=0) with a non-moving GC, so the
// framebuffer's Go slice is the resource's guest backing store at its own
// address - one contiguous virtio_gpu_mem_entry, no scatter list.

//go:build tamago && riscv64

package virt

import (
	"encoding/binary"
	"errors"
	"image"
	"runtime"
	"sync"
)

const (
	virtioDevGPU = 16 // virtio subsystem device ID for the GPU
	gpuCtrlQ     = 0  // controlq (cursorq is queue 1; honk does not use it)
	gpuQueueLen  = 8

	// control command and response types (the 2D subset honk uses; the full
	// set is in the virtio-gpu spec).
	gpuCmdGetDisplayInfo   = 0x0100
	gpuCmdResourceCreate2D = 0x0101
	gpuCmdSetScanout       = 0x0103
	gpuCmdResourceFlush    = 0x0104
	gpuCmdTransferToHost2D = 0x0105
	gpuCmdAttachBacking    = 0x0106
	gpuRespOKNoData        = 0x1100
	gpuRespOKDisplayInfo   = 0x1101

	// VIRTIO_GPU_FORMAT_R8G8B8A8_UNORM: memory byte order R,G,B,A, identical to
	// the stdlib image.RGBA pixel layout, so drawing into the surface needs no
	// channel swizzle before the transfer to the host.
	gpuFormatR8G8B8A8 = 67

	gpuHdrLen     = 24  // struct virtio_gpu_ctrl_hdr
	gpuRespLen    = 512 // generous: the largest reply (display info) is 24+16*24=408
	gpuResourceID = 1   // honk drives a single full-screen scanout resource

	// fallback resolution when the device reports no enabled display (which is
	// what QEMU does headless, under -display none); the spec sanctions it.
	gpuFallbackW = 1024
	gpuFallbackH = 768
)

var errGPU = errors.New("virtio-gpu: device error")

// GPU is honk's virtio-gpu framebuffer. Image() is the drawable surface and
// Flush() pushes it to the display; everything else is hidden.
type GPU struct {
	dev  vioDev
	q    vioQueue
	w, h int

	img *image.RGBA // drawable surface; its Pix is the DMA framebuffer
	fb  []byte      // == img.Pix: the scanout resource's guest backing store

	mu   sync.Mutex // serializes control commands (one in-flight exchange)
	req  []byte     // device-readable command buffer
	resp []byte     // device-writable reply buffer
}

// ProbeGPU scans the virtio-mmio slots for a GPU, brings up a single
// full-screen scanout resource the size of display 0, and returns it - or nil
// if no GPU is present.
func ProbeGPU() *GPU {
	for i := 0; i < virtioSlots; i++ {
		dev, ok := vioProbe(i, virtioDevGPU)
		if !ok {
			continue
		}
		g := &GPU{dev: dev}
		if g.init() {
			println("honk: display = virtio-gpu,", g.w, "x", g.h)
			return g
		}
	}
	return nil
}

// Image returns the drawable framebuffer surface. Drawing into it with
// image/draw is local (it writes the resource's guest backing); Flush makes the
// result visible.
func (g *GPU) Image() *image.RGBA { return g.img }

// Flush copies the whole framebuffer to the host resource and flushes it to the
// scanout, making prior drawing visible. It is the only post-init caller of the
// control queue, so it serializes with itself via g.mu.
func (g *GPU) Flush() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	// TRANSFER_TO_HOST_2D: hdr, rect{0,0,w,h}, offset(8), resource_id.
	clear(g.req[:56])
	binary.LittleEndian.PutUint32(g.req[0:], gpuCmdTransferToHost2D)
	binary.LittleEndian.PutUint32(g.req[32:], uint32(g.w)) // rect.width
	binary.LittleEndian.PutUint32(g.req[36:], uint32(g.h)) // rect.height
	binary.LittleEndian.PutUint32(g.req[48:], gpuResourceID)
	if err := g.ctrl(56, gpuRespOKNoData); err != nil {
		return err
	}

	// RESOURCE_FLUSH: hdr, rect{0,0,w,h}, resource_id.
	clear(g.req[:48])
	binary.LittleEndian.PutUint32(g.req[0:], gpuCmdResourceFlush)
	binary.LittleEndian.PutUint32(g.req[32:], uint32(g.w))
	binary.LittleEndian.PutUint32(g.req[36:], uint32(g.h))
	binary.LittleEndian.PutUint32(g.req[40:], gpuResourceID)
	return g.ctrl(48, gpuRespOKNoData)
}

func (g *GPU) init() bool {
	if _, ok := g.dev.negotiate(virtioFVersion1); !ok {
		return false
	}
	if !g.q.setup(g.dev, gpuCtrlQ, gpuQueueLen) {
		return false
	}
	g.req = dmaAlloc(256, 1)
	g.resp = dmaAlloc(gpuRespLen, 1)
	g.dev.ready()

	g.w, g.h = g.displayInfo()
	g.fb = dmaAlloc(g.w*g.h*4, 4096)
	g.img = &image.RGBA{Pix: g.fb, Stride: g.w * 4, Rect: image.Rect(0, 0, g.w, g.h)}

	// Create the scanout resource, back it with the framebuffer, and link it to
	// scanout 0. The initial (zeroed) framebuffer is transferred by the first
	// Flush.
	return g.createResource() && g.attachBacking() && g.setScanout()
}

// displayInfo returns the size of scanout 0, or the fallback resolution if the
// device reports no enabled display (the headless case).
func (g *GPU) displayInfo() (w, h int) {
	clear(g.req[:gpuHdrLen])
	binary.LittleEndian.PutUint32(g.req[0:], gpuCmdGetDisplayInfo)
	if g.ctrl(gpuHdrLen, gpuRespOKDisplayInfo) == nil {
		// pmodes[0] follows the reply header: rect{x,y,width,height}, enabled.
		w = int(binary.LittleEndian.Uint32(g.resp[gpuHdrLen+8:]))
		h = int(binary.LittleEndian.Uint32(g.resp[gpuHdrLen+12:]))
		enabled := binary.LittleEndian.Uint32(g.resp[gpuHdrLen+16:])
		if enabled != 0 && w > 0 && h > 0 {
			return w, h
		}
	}
	return gpuFallbackW, gpuFallbackH
}

func (g *GPU) createResource() bool {
	clear(g.req[:40])
	binary.LittleEndian.PutUint32(g.req[0:], gpuCmdResourceCreate2D)
	binary.LittleEndian.PutUint32(g.req[24:], gpuResourceID)
	binary.LittleEndian.PutUint32(g.req[28:], gpuFormatR8G8B8A8)
	binary.LittleEndian.PutUint32(g.req[32:], uint32(g.w))
	binary.LittleEndian.PutUint32(g.req[36:], uint32(g.h))
	return g.ctrl(40, gpuRespOKNoData) == nil
}

func (g *GPU) attachBacking() bool {
	clear(g.req[:48])
	binary.LittleEndian.PutUint32(g.req[0:], gpuCmdAttachBacking)
	binary.LittleEndian.PutUint32(g.req[24:], gpuResourceID)
	binary.LittleEndian.PutUint32(g.req[28:], 1) // nr_entries: one contiguous region
	binary.LittleEndian.PutUint64(g.req[32:], ptr(g.fb))
	binary.LittleEndian.PutUint32(g.req[40:], uint32(len(g.fb)))
	return g.ctrl(48, gpuRespOKNoData) == nil
}

func (g *GPU) setScanout() bool {
	clear(g.req[:48])
	binary.LittleEndian.PutUint32(g.req[0:], gpuCmdSetScanout)
	binary.LittleEndian.PutUint32(g.req[32:], uint32(g.w)) // rect.width
	binary.LittleEndian.PutUint32(g.req[36:], uint32(g.h)) // rect.height
	binary.LittleEndian.PutUint32(g.req[40:], 0)           // scanout_id 0
	binary.LittleEndian.PutUint32(g.req[44:], gpuResourceID)
	return g.ctrl(48, gpuRespOKNoData) == nil
}

// ctrl publishes the command in g.req[:reqLen], waits for the reply in g.resp,
// and checks the reply type. The caller has filled g.req and (post-init) holds
// g.mu.
func (g *GPU) ctrl(reqLen int, want uint32) error {
	g.q.setDesc(0, ptr(g.req), uint32(reqLen), descNext, 1)
	g.q.setDesc(1, ptr(g.resp), uint32(len(g.resp)), descWrite, 0)
	g.q.offer(0)
	fence()
	g.dev.notify(gpuCtrlQ)

	for spins := 0; ; spins++ {
		if _, _, ok := g.q.take(); ok {
			break
		}
		if spins > 1<<24 {
			return errGPU
		}
		runtime.Gosched()
	}
	g.dev.ackIRQ()

	if binary.LittleEndian.Uint32(g.resp[0:]) != want {
		return errGPU
	}
	return nil
}
