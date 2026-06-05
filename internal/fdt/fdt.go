// Package fdt is a minimal parser for the Flattened Device Tree blob that RISC-V
// firmware passes to the kernel (pointer in a1 at boot). It discovers hardware —
// RAM, harts, the timer frequency, device MMIO bases — so one image can boot
// many machines instead of hardcoding addresses (RV64.md Part 7.1).
//
// Everything in the blob is big-endian. The package is deliberately pure Go (no
// build constraints, no unsafe) so it unit-tests on the host (GO.md §16); the
// kernel side only has to hand it a []byte.
package fdt

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

const magic = 0xd00dfeed

// struct-block tokens
const (
	tokBeginNode = 1
	tokEndNode   = 2
	tokProp      = 3
	tokNop       = 4
	tokEnd       = 9
)

// Node is a device-tree node: a name, its properties (raw big-endian bytes), and
// its children.
type Node struct {
	Name     string
	Children []*Node
	props    map[string][]byte
}

// Tree is a parsed device tree.
type Tree struct {
	Root *Node
}

// Parse decodes a flattened device tree. It never panics on malformed input —
// firmware data is untrusted — returning an error instead.
func Parse(b []byte) (*Tree, error) {
	if len(b) < 40 {
		return nil, errors.New("fdt: blob shorter than header")
	}
	if be32(b) != magic {
		return nil, fmt.Errorf("fdt: bad magic 0x%08x", be32(b))
	}
	structOff, stringsOff := be32(b[8:]), be32(b[12:])
	structSize := be32(b[36:])
	if int(structOff) > len(b) || int(stringsOff) > len(b) {
		return nil, errors.New("fdt: struct/strings offset out of range")
	}
	s := b[structOff:]
	if int(structSize) <= len(s) {
		s = s[:structSize]
	}
	strs := b[stringsOff:]

	pos := 0
	read32 := func() (uint32, bool) {
		if pos+4 > len(s) {
			return 0, false
		}
		v := be32(s[pos:])
		pos += 4
		return v, true
	}

	var root *Node
	var stack []*Node
	for {
		tok, ok := read32()
		if !ok {
			return nil, errors.New("fdt: truncated struct block")
		}
		switch tok {
		case tokBeginNode:
			start := pos
			for pos < len(s) && s[pos] != 0 {
				pos++
			}
			if pos >= len(s) {
				return nil, errors.New("fdt: unterminated node name")
			}
			n := &Node{Name: string(s[start:pos]), props: map[string][]byte{}}
			pos = align4(pos + 1) // skip NUL, pad to 4
			if len(stack) == 0 {
				root = n
			} else {
				p := stack[len(stack)-1]
				p.Children = append(p.Children, n)
			}
			stack = append(stack, n)
		case tokEndNode:
			if len(stack) == 0 {
				return nil, errors.New("fdt: unbalanced end-node")
			}
			stack = stack[:len(stack)-1]
		case tokProp:
			plen, ok1 := read32()
			nameoff, ok2 := read32()
			if !ok1 || !ok2 || pos+int(plen) > len(s) {
				return nil, errors.New("fdt: property overflows struct block")
			}
			val := append([]byte(nil), s[pos:pos+int(plen)]...)
			pos = align4(pos + int(plen))
			if len(stack) > 0 {
				stack[len(stack)-1].props[cstr(strs, int(nameoff))] = val
			}
		case tokNop:
		case tokEnd:
			if root == nil {
				return nil, errors.New("fdt: empty tree")
			}
			return &Tree{Root: root}, nil
		default:
			return nil, fmt.Errorf("fdt: unknown token %d", tok)
		}
	}
}

// --- node accessors ---

// Prop returns the raw (big-endian) bytes of a property.
func (n *Node) Prop(name string) ([]byte, bool) { v, ok := n.props[name]; return v, ok }

// PropString returns the first NUL-terminated string of a property.
func (n *Node) PropString(name string) (string, bool) {
	v, ok := n.props[name]
	if !ok {
		return "", false
	}
	if i := bytes.IndexByte(v, 0); i >= 0 {
		v = v[:i]
	}
	return string(v), true
}

// PropU32 returns a property's first big-endian uint32 cell.
func (n *Node) PropU32(name string) (uint32, bool) {
	v, ok := n.props[name]
	if !ok || len(v) < 4 {
		return 0, false
	}
	return be32(v), true
}

// Child returns the first direct child with the given name.
func (n *Node) Child(name string) *Node {
	for _, c := range n.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func (n *Node) propU32Default(name string, d uint32) uint32 {
	if v, ok := n.PropU32(name); ok {
		return v
	}
	return d
}

// enabled reports whether a node is usable. A missing "status" property means
// "okay" per the Devicetree Specification; "disabled"/"fail"/"reserved" nodes
// are skipped by the high-level queries below.
func (n *Node) enabled() bool {
	s, ok := n.PropString("status")
	return !ok || s == "okay" || s == "ok"
}

// --- high-level queries ---

// Model returns the root "model" string (the board name), or "".
func (t *Tree) Model() string { m, _ := t.Root.PropString("model"); return m }

// Memory returns the base and size of the first /memory node, decoded with the
// root's #address-cells/#size-cells (spec defaults 2 and 1).
func (t *Tree) Memory() (base, size uint64, ok bool) {
	ac := t.Root.propU32Default("#address-cells", 2)
	sc := t.Root.propU32Default("#size-cells", 1)
	for _, c := range t.Root.Children {
		dt, _ := c.PropString("device_type")
		if dt != "memory" && !strings.HasPrefix(c.Name, "memory@") {
			continue
		}
		if !c.enabled() {
			continue
		}
		reg, ok2 := c.Prop("reg")
		if !ok2 || len(reg) < int(ac+sc)*4 {
			return 0, 0, false
		}
		return readCells(reg[:ac*4]), readCells(reg[ac*4 : (ac+sc)*4]), true
	}
	return 0, 0, false
}

// TimebaseFrequency returns the /cpus timebase-frequency in Hz (the `time` CSR
// tick rate), checking the node itself and then each cpu child.
func (t *Tree) TimebaseFrequency() (uint32, bool) {
	cpus := t.Root.Child("cpus")
	if cpus == nil {
		return 0, false
	}
	if v, ok := cpus.PropU32("timebase-frequency"); ok {
		return v, true
	}
	for _, c := range cpus.Children {
		if v, ok := c.PropU32("timebase-frequency"); ok {
			return v, true
		}
	}
	return 0, false
}

// HartHasExtension reports whether any /cpus cpu node advertises the named ISA
// extension, via either "riscv,isa-extensions" (a device-tree stringlist) or
// "riscv,isa" (an underscore-separated ISA string, e.g. "rv64imafdc_sstc").
// Used to detect Sstc (direct S-mode stimecmp timer) before relying on it.
func (t *Tree) HartHasExtension(name string) bool {
	cpus := t.Root.Child("cpus")
	if cpus == nil {
		return false
	}
	for _, c := range cpus.Children {
		if dt, _ := c.PropString("device_type"); dt != "cpu" || !c.enabled() {
			continue
		}
		if v, ok := c.Prop("riscv,isa-extensions"); ok {
			for _, ext := range stringList(v) {
				if ext == name {
					return true
				}
			}
		}
		if isa, ok := c.PropString("riscv,isa"); ok {
			for _, tok := range strings.Split(isa, "_") {
				if tok == name {
					return true
				}
			}
		}
	}
	return false
}

// HartCount returns the number of enabled harts (device_type="cpu" under /cpus).
func (t *Tree) HartCount() int {
	cpus := t.Root.Child("cpus")
	if cpus == nil {
		return 0
	}
	n := 0
	for _, c := range cpus.Children {
		if dt, _ := c.PropString("device_type"); dt == "cpu" && c.enabled() {
			n++
		}
	}
	return n
}

// --- decoding helpers ---

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// readCells reads len(b)/4 big-endian uint32 cells as one big-endian value.
func readCells(b []byte) uint64 {
	var v uint64
	for i := 0; i+4 <= len(b); i += 4 {
		v = v<<32 | uint64(be32(b[i:]))
	}
	return v
}

func align4(n int) int { return (n + 3) &^ 3 }

// stringList splits a device-tree stringlist property (concatenated
// NUL-terminated strings) into its elements.
func stringList(b []byte) []string {
	var out []string
	start := 0
	for i, c := range b {
		if c == 0 {
			if i > start {
				out = append(out, string(b[start:i]))
			}
			start = i + 1
		}
	}
	return out
}

func cstr(b []byte, off int) string {
	if off < 0 || off >= len(b) {
		return ""
	}
	e := off
	for e < len(b) && b[e] != 0 {
		e++
	}
	return string(b[off:e])
}
