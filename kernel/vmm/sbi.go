package vmm

// SBI is the firmware ABI a RISC-V supervisor calls via `ecall` (RV64.md §2).
// A guest in VS-mode makes these calls expecting the environment (here, honk's
// VMM) to service them; this file is the single owner of the wire numbers -
// extension ids (a7), function ids (a6), and error codes - that honk's SBI
// emulator (board/virt/vmm.go) dispatches on. Keeping the numbers here lets the
// guest programs (which invoke them) and the emulator (which services them)
// share one definition, and lets the guest encoding be host-tested.

// SBI legacy extension ids (the v0.1 calls: the id is in a7, there is no FID,
// and only a0 is returned). honk's first guests use these for the console.
const (
	SBIConsolePutchar = 0x01 // console_putchar(a0): one byte to the console
	SBIShutdown       = 0x08 // shutdown: the guest's "done" signal
)

// SBI v0.2+ extension ids (spelled in ASCII; a7=EID, a6=FID, a0..a5 args,
// returning a0=error, a1=value). honk emulates the small subset its guests use.
const (
	SBIExtBase = 0x10       // Base: discovery (probe extensions, versions)
	SBIExtTime = 0x54494D45 // "TIME": the supervisor timer
)

// Function ids within the extensions above.
const (
	SBIBaseProbeExtension = 3 // Base: probe_extension(a0=EID) -> 0 absent, !=0 present
	SBITimeSetTimer       = 0 // TIME: set_timer(a0=absolute time) -> arms the next timer
)

// SBI error codes returned in a0 (a subset; 0 is success).
const (
	SBISuccess         = 0
	SBIErrNotSupported = -2
)
