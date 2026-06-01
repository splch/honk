// Package device is Honk's driver registry: it maps a device-tree "compatible"
// string to the driver that handles that hardware, so a board selects a driver
// by what the firmware reports rather than by a hardcoded type. A fork adds
// support for new hardware by registering its driver here from an init function
// — the board and boot code never change.
package device

import "github.com/splch/honk/kernel/console"

// consoleDrivers maps a device-tree "compatible" string to a constructor for a
// console byte device at a given MMIO base address.
var consoleDrivers = map[string]func(base uintptr) console.Device{}

// RegisterConsole registers ctor as the driver for console UART hardware that
// reports the device-tree "compatible" string compat. Drivers call this from an
// init function; registering the same string twice keeps the last registration.
func RegisterConsole(compat string, ctor func(base uintptr) console.Device) {
	consoleDrivers[compat] = ctor
}

// ConsoleDriver returns the constructor registered for compat, if any.
func ConsoleDriver(compat string) (func(base uintptr) console.Device, bool) {
	ctor, ok := consoleDrivers[compat]
	return ctor, ok
}
