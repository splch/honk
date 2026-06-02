//go:build tamago && riscv64

package virt

import (
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
	now := time.Now().Add(r.ClockOffset)
	SetWallClock(now.UnixNano())
	return now, nil
}
