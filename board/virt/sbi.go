// honk - QEMU virt board: SBI (Supervisor Binary Interface) calls.
//
// honk runs in HS-mode and asks the M-mode firmware (OpenSBI) for services via
// `ecall`. These wrappers are implemented in boot_riscv64.s; see RV64.md §2.

//go:build tamago && riscv64

package virt

// SBI extension IDs (EIDs), spelled in ASCII per the SBI spec.
const (
	sbiExtSRST = 0x53525354 // "SRST" - System Reset
	sbiExtHSM  = 0x48534D   // "HSM"  - Hart State Management
)

// sbiPutchar writes one byte to the console via the SBI legacy console_putchar
// call (EID 0x01).
func sbiPutchar(c byte)

// sbiCall issues a generic SBI v0.2+ ecall (EID, FID, up to three args) and
// returns the (error, value) pair from a0/a1.
func sbiCall(eid, fid, arg0, arg1, arg2 uint64) (err int64, val int64)

// readTime reads the RISC-V time CSR (rdtime), in timebase ticks.
func readTime() uint64
