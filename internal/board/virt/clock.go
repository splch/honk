//go:build tamago && riscv64

package virt

import (
	"fmt"
	"strconv"
	"time"

	"github.com/beevik/ntp"
)

// buildUnixStr is the build time in Unix seconds, injected via
// `-ldflags -X` (see the Makefile). honk has no real-time clock, so this seeds a
// plausible wall time at boot — recent enough for TLS certificate validation —
// which NTP can later refine.
var buildUnixStr string

// initClock seeds the wall clock from the build time. It runs in hwinit1, before
// gVisor and its timers start, so the later (small) NTP correction never jumps
// the shared monotonic/wall clock by decades (DESIGN.md §15.5).
//
// QEMU virt does expose a Goldfish RTC (a more accurate seed), but it sits at
// 0x101000 — inside the first 2 MiB that vm.go deliberately leaves unmapped as a
// nil-pointer guard — so honk cannot read it once paging is on, and the build
// time is the best available seed.
func initClock() {
	if buildUnixStr == "" {
		return
	}
	sec, err := strconv.ParseInt(buildUnixStr, 10, 64)
	if err != nil {
		return
	}
	SetWallClock(sec * int64(time.Second))
	puts("honk/virt: clock seeded from build time ")
	puts(time.Unix(sec, 0).UTC().Format(time.RFC3339))
	puts("\n")
}

// ntpSync queries an SNTP server over the gVisor stack and sets the wall clock
// to the result. The default beevik dialer uses net.Dial, which honk routes
// through gVisor via net.SocketFunc; the small offset (the boot floor is already
// ~correct) keeps the clock step harmless.
func ntpSync(host string) (time.Time, error) {
	r, err := ntp.Query(host)
	if err != nil {
		return time.Time{}, err
	}
	// Reject a malformed or hostile response (bad stratum, kiss-o'-death, zero
	// transmit time, excessive dispersion) before letting it move the clock.
	if err := r.Validate(); err != nil {
		return time.Time{}, fmt.Errorf("ntp: response failed validation: %w", err)
	}
	// honk's wall clock and the runtime's monotonic clock are the SAME counter
	// (tamago exposes no separate walltime seam), so an NTP correction also moves
	// monotonic time — which Go assumes never happens (time.Since, time.Timer, and
	// gVisor's TCP RTO all read it). The build-time seed already puts the clock
	// within a small error, so a legitimate correction is tiny in either
	// direction. Only ever STEP a small amount: refuse a large backward step
	// (would make time.Since negative and stall armed timers) and a large forward
	// step (would fast-forward every pending deadline). True slewing of small
	// offsets is future work; a bounded step is the safe compromise.
	const (
		maxBackstep    = 2 * time.Second
		maxForwardStep = 10 * time.Minute
	)
	switch {
	case r.ClockOffset < -maxBackstep:
		return time.Time{}, fmt.Errorf("ntp: backward offset %s exceeds %s; refusing (shared monotonic clock)",
			r.ClockOffset.Round(time.Millisecond), maxBackstep)
	case r.ClockOffset > maxForwardStep:
		return time.Time{}, fmt.Errorf("ntp: forward offset %s exceeds %s; refusing (shared monotonic clock)",
			r.ClockOffset.Round(time.Second), maxForwardStep)
	}
	now := time.Now().Add(r.ClockOffset)
	SetWallClock(now.UnixNano())
	return now, nil
}
