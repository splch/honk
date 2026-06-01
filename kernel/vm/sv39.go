// Package vm implements RISC-V Sv39 virtual memory: the page-table format Honk
// uses to give each user process its own address space.
//
// Sv39 translates a 39-bit virtual address through a three-level radix tree of
// 512-entry tables. A virtual address splits into three 9-bit indices (one per
// level) plus a 12-bit page offset:
//
//	38       30 29      21 20      12 11         0
//	+----------+----------+----------+------------+
//	|  VPN[2]  |  VPN[1]  |  VPN[0]  |   offset   |
//	+----------+----------+----------+------------+
//
// Translation walks VPN[2] -> VPN[1] -> VPN[0]; a leaf entry (any of R/W/X set)
// holds the physical page, while a non-leaf entry points at the next table.
//
// Every type and helper here is pure logic with no hardware access, so the whole
// package is unit-tested on the host with `go test ./kernel/vm` — something you
// cannot do with a C teaching kernel.
package vm

import "unsafe"

// PageSize is the Sv39 base page (and page-table) size.
const PageSize = 4096

// PTE is a Sv39 page-table entry: a packed physical page number plus flag bits.
type PTE uint64

// PTE permission and status flag bits (RISC-V privileged spec, Sv39).
const (
	Valid    PTE = 1 << 0 // entry is valid
	Read     PTE = 1 << 1 // readable
	Write    PTE = 1 << 2 // writable
	Exec     PTE = 1 << 3 // executable
	User     PTE = 1 << 4 // accessible from user mode (and NOT executable by S-mode)
	Global   PTE = 1 << 5 // global mapping
	Accessed PTE = 1 << 6 // set by hardware on access (Honk presets it)
	Dirty    PTE = 1 << 7 // set by hardware on write (Honk presets it)

	flagMask PTE = 0x3FF // low 10 bits are flags; bits 10+ are the PPN
)

// Leaf reports whether the entry maps a page (R, W, or X set) rather than
// pointing at a next-level table.
func (p PTE) Leaf() bool { return p&(Read|Write|Exec) != 0 }

// PA returns the physical address the entry refers to.
func (p PTE) PA() uintptr { return uintptr(p>>10) << 12 }

// Flags returns just the permission/status bits.
func (p PTE) Flags() PTE { return p & flagMask }

// makePTE packs a (page-aligned) physical address and flags into a valid entry.
func makePTE(pa uintptr, flags PTE) PTE { return PTE(pa>>12)<<10 | flags | Valid }

// vpn returns the 9-bit page-table index for level (2, 1, or 0) of va.
func vpn(level int, va uintptr) int { return int(va>>(12+9*level)) & 0x1FF }

// MakeSATP builds the satp CSR value that activates the Sv39 table rooted at the
// physical address root (MODE=8 in the top nibble, root PPN in the low bits).
func MakeSATP(root uintptr) uint64 { return 8<<60 | uint64(root>>12) }

// Table is one 512-entry Sv39 page table — exactly one 4 KiB page.
type Table [512]PTE

// Mapper builds an Sv39 page table, allocating intermediate tables on demand. It
// keeps every table it allocates alive (so the physical addresses stored inside
// PTEs stay valid under the host GC during tests; in the kernel the tables come
// from a fixed-region page allocator).
type Mapper struct {
	Root  *Table
	pages []*Table
}

// NewMapper returns a Mapper with a fresh, empty root table.
func NewMapper() *Mapper {
	m := &Mapper{}
	m.Root = m.alloc()
	return m
}

func (m *Mapper) alloc() *Table {
	t := new(Table)
	m.pages = append(m.pages, t)
	return t
}

// Map installs a leaf mapping of the page at virtual address va to the physical
// page pa with the given permission flags (Valid, Accessed, and Dirty are added
// automatically). va and pa must be page-aligned.
func (m *Mapper) Map(va, pa uintptr, flags PTE) {
	t := m.Root
	for level := 2; level > 0; level-- {
		e := &t[vpn(level, va)]
		if *e&Valid == 0 {
			*e = makePTE(physOf(m.alloc()), 0) // pointer entry: Valid, no R/W/X
		}
		t = tableAt(e.PA())
	}
	t[vpn(0, va)] = makePTE(pa, flags|Accessed|Dirty)
}

// Lookup walks the table for va and returns the leaf PTE and whether va is
// mapped — the software twin of the hardware page-table walk.
func (m *Mapper) Lookup(va uintptr) (PTE, bool) {
	t := m.Root
	for level := 2; level >= 0; level-- {
		e := t[vpn(level, va)]
		if e&Valid == 0 {
			return 0, false
		}
		if e.Leaf() {
			return e, true
		}
		t = tableAt(e.PA())
	}
	return 0, false
}

// physOf/tableAt convert between a Go *Table and the physical address stored in
// a PTE. Honk runs with physical == virtual for kernel memory (no kernel paging;
// see the package design notes), so this identity holds both in tests and on the
// machine.
func physOf(t *Table) uintptr   { return uintptr(unsafe.Pointer(t)) }
func tableAt(pa uintptr) *Table { return (*Table)(unsafe.Pointer(pa)) }
