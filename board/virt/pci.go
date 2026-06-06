// honk - QEMU virt board: minimal PCIe (ECAM) enumeration.
//
// Just enough to find a device by class, assign its BAR0 in the MMIO window,
// and enable it. OpenSBI does not assign PCIe BARs, so honk does.

//go:build tamago && riscv64

package virt

const (
	pcieECAM = 0x30000000 // ECAM config space (RV64.md App. A)
	pcieMMIO = 0x40000000 // 32-bit MMIO window for BAR assignment

	pciVendorID = 0x00 // u16
	pciCommand  = 0x04 // u16
	pciClassRev = 0x08 // u32: [31:8] class/subclass/prog-if, [7:0] revision
	pciBAR0     = 0x10

	pciCmdMemory    = 1 << 1 // respond to memory space
	pciCmdBusMaster = 1 << 2 // act as DMA bus master
)

// pciAddr returns the ECAM address of a config register for bus/dev/fn.
func pciAddr(bus, dev, fn, off uint32) uintptr {
	return uintptr(pcieECAM) + uintptr(bus<<20|dev<<15|fn<<12|off)
}

func pciRead32(bus, dev, fn, off uint32) uint32 { return mmioRead32(pciAddr(bus, dev, fn, off)) }
func pciWrite32(bus, dev, fn, off, v uint32)    { mmioWrite32(pciAddr(bus, dev, fn, off), v) }

// pciFindByClass scans bus 0 (function 0) for the first device whose 24-bit
// class code (class:subclass:prog-if) matches, returning its device number.
func pciFindByClass(class uint32) (dev uint32, found bool) {
	for d := uint32(0); d < 32; d++ {
		if pciRead32(0, d, 0, pciVendorID)&0xffff == 0xffff {
			continue // no device
		}
		if pciRead32(0, d, 0, pciClassRev)>>8 == class {
			return d, true
		}
	}
	return 0, false
}

// pciSetupBAR0 assigns a 64-bit memory BAR0 at addr and enables memory-space
// and bus-master access. addr must be aligned to the BAR size (pcieMMIO is
// far more aligned than any small device BAR).
func pciSetupBAR0(dev uint32, addr uint64) {
	pciWrite32(0, dev, 0, pciBAR0, uint32(addr))
	pciWrite32(0, dev, 0, pciBAR0+4, uint32(addr>>32))

	cmd := pciRead32(0, dev, 0, pciCommand)
	pciWrite32(0, dev, 0, pciCommand, cmd|pciCmdMemory|pciCmdBusMaster)
}
