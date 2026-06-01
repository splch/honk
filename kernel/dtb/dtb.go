// Package dtb parses the Flattened Device Tree (FDT) blob that the firmware
// passes to the kernel at boot, so Honk can *discover* its hardware — the
// console UART, the amount of RAM, the number of harts — instead of hardcoding
// addresses for one machine. Discovery is what lets the same kernel image boot
// on QEMU and on real RISC-V boards, and it is the seam a fork extends to
// support new hardware.
//
// The format is the one in the Devicetree Specification: a header, a struct
// block of nested nodes and properties, and a strings block the property names
// index into. Every node carries a name, a set of properties, and children; a
// node's `reg` (its address/size) is decoded with its parent's #address-cells
// and #size-cells.
//
// The whole package is pure logic with no hardware access, so it is unit-tested
// on the host with `go test ./kernel/dtb`. A truncated or corrupt blob can only
// drive an out-of-bounds slice access, which Go turns into a panic that Parse
// recovers as "not a device tree" — memory safety means a bad blob is a clean
// no-op, not an exploit.
package dtb

import "strings"

// FDT header magic and struct-block token types (Devicetree Specification).
const (
	magic = 0xd00dfeed

	tokBeginNode = 0x1
	tokEndNode   = 0x2
	tokProp      = 0x3
	tokNop       = 0x4
	tokEnd       = 0x9
)

// Tree is a parsed device tree, queried from its root.
type Tree struct{ root *Node }

// Node is one device-tree node: its name (with unit address, e.g.
// "serial@10000000"), its properties, and its children. regAddrCells and
// regSizeCells are the parent's cell counts, used to decode this node's reg.
type Node struct {
	Name         string
	props        map[string][]byte
	children     []*Node
	regAddrCells int
	regSizeCells int
}

// Parse decodes an FDT blob and reports whether it was a valid device tree. A
// nil, empty, or malformed blob yields (nil, false), which callers treat as "no
// device tree, use defaults" — there is no error to handle.
func Parse(blob []byte) (t *Tree, ok bool) {
	// A truncated blob can only cause an out-of-bounds read; recover it as a
	// clean parse failure rather than a kernel panic.
	defer func() {
		if recover() != nil {
			t, ok = nil, false
		}
	}()
	if len(blob) < 40 || be32(blob, 0) != magic {
		return nil, false
	}
	p := &parser{blob: blob, strings: int(be32(blob, 12)), pos: int(be32(blob, 8))}
	return &Tree{root: p.node(2, 1)}, true // root's reg uses the spec defaults (and is unused)
}

// parser walks the struct block.
type parser struct {
	blob    []byte
	strings int // offset of the strings block
	pos     int // cursor into blob, within the struct block
}

// node parses one node (the cursor sits on its FDT_BEGIN_NODE token); regAddr
// and regSize are the cell counts to decode this node's own reg.
func (p *parser) node(regAddr, regSize int) *Node {
	p.pos += 4 // FDT_BEGIN_NODE
	n := &Node{
		Name:         p.cstr(),
		props:        map[string][]byte{},
		regAddrCells: regAddr,
		regSizeCells: regSize,
	}
	p.align()
	for {
		switch be32(p.blob, p.pos) {
		case tokProp:
			length := int(be32(p.blob, p.pos+4))
			name := p.strAt(int(be32(p.blob, p.pos+8)))
			p.pos += 12
			n.props[name] = p.blob[p.pos : p.pos+length]
			p.pos += length
			p.align()
		case tokBeginNode:
			n.children = append(n.children, p.node(n.cells("#address-cells", 2), n.cells("#size-cells", 1)))
		case tokNop:
			p.pos += 4
		default: // FDT_END_NODE or FDT_END
			p.pos += 4
			return n
		}
	}
}

// cstr reads the null-terminated string at the cursor and advances past it.
func (p *parser) cstr() string {
	start := p.pos
	for p.blob[p.pos] != 0 {
		p.pos++
	}
	s := string(p.blob[start:p.pos])
	p.pos++ // the null
	return s
}

// strAt reads the null-terminated property name at off into the strings block.
func (p *parser) strAt(off int) string {
	b := p.blob[p.strings+off:]
	end := 0
	for b[end] != 0 {
		end++
	}
	return string(b[:end])
}

func (p *parser) align() { p.pos = (p.pos + 3) &^ 3 }

// --- queries ---

// Node returns the node at an absolute path ("/", "/soc/serial@10000000") and
// whether it exists.
func (t *Tree) Node(path string) (*Node, bool) {
	n := t.root
	for _, part := range strings.Split(strings.Trim(path, "/"), "/") {
		if part == "" {
			continue // the root path "/"
		}
		c, ok := n.child(part)
		if !ok {
			return nil, false
		}
		n = c
	}
	return n, true
}

// Compatible returns the first node (depth-first) whose "compatible" list
// includes compat, and whether one was found.
func (t *Tree) Compatible(compat string) (*Node, bool) { return t.root.find(compat) }

// Stdout returns the console node named by /chosen's "stdout-path" — the device
// tree's own answer to "where does the console go" — and whether it resolved. A
// path may be an /aliases name and may carry a ":baud" suffix; both are handled.
func (t *Tree) Stdout() (*Node, bool) {
	chosen, ok := t.Node("/chosen")
	if !ok {
		return nil, false
	}
	path, ok := chosen.str("stdout-path")
	if !ok {
		return nil, false
	}
	path, _, _ = strings.Cut(path, ":")
	if !strings.HasPrefix(path, "/") {
		if aliases, ok := t.Node("/aliases"); ok {
			if target, ok := aliases.str(path); ok {
				path = target
			}
		}
	}
	return t.Node(path)
}

// Memory returns the base address and size in bytes of the first /memory node.
func (t *Tree) Memory() (base, size uintptr, ok bool) {
	for _, c := range t.root.children {
		if dt, ok := c.str("device_type"); ok && dt == "memory" {
			return c.Reg()
		}
	}
	return 0, 0, false
}

// NumCPUs returns the number of usable harts: cpu nodes under /cpus whose status
// is not "disabled".
func (t *Tree) NumCPUs() int {
	cpus, ok := t.Node("/cpus")
	if !ok {
		return 0
	}
	n := 0
	for _, c := range cpus.children {
		if dt, ok := c.str("device_type"); !ok || dt != "cpu" {
			continue
		}
		if st, ok := c.str("status"); ok && (st == "disabled" || st == "fail") {
			continue
		}
		n++
	}
	return n
}

// Compatibles returns the node's "compatible" strings, most-specific first.
func (n *Node) Compatibles() []string {
	v, ok := n.props["compatible"]
	if !ok {
		return nil
	}
	var out []string
	for _, s := range strings.Split(string(v), "\x00") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Compatible reports whether the node's "compatible" list includes compat.
func (n *Node) Compatible(compat string) bool {
	for _, c := range n.Compatibles() {
		if c == compat {
			return true
		}
	}
	return false
}

// Reg returns the node's first (address, size) pair from its "reg" property,
// decoded with the parent's address/size cells, and whether reg is present.
func (n *Node) Reg() (addr, size uintptr, ok bool) {
	v, ok := n.props["reg"]
	if !ok || len(v) < (n.regAddrCells+n.regSizeCells)*4 {
		return 0, 0, false
	}
	for i := 0; i < n.regAddrCells; i++ {
		addr = addr<<32 | uintptr(be32(v, i*4))
	}
	for i := 0; i < n.regSizeCells; i++ {
		size = size<<32 | uintptr(be32(v, (n.regAddrCells+i)*4))
	}
	return addr, size, true
}

// Prop returns the raw bytes of property name and whether it is present.
func (n *Node) Prop(name string) ([]byte, bool) {
	v, ok := n.props[name]
	return v, ok
}

func (n *Node) child(name string) (*Node, bool) {
	for _, c := range n.children {
		if c.Name == name {
			return c, true
		}
	}
	return nil, false
}

func (n *Node) find(compat string) (*Node, bool) {
	if n.Compatible(compat) {
		return n, true
	}
	for _, c := range n.children {
		if r, ok := c.find(compat); ok {
			return r, ok
		}
	}
	return nil, false
}

// str returns property name as a string up to its first null, and whether it
// is present.
func (n *Node) str(name string) (string, bool) {
	v, ok := n.props[name]
	if !ok {
		return "", false
	}
	s, _, _ := strings.Cut(string(v), "\x00")
	return s, true
}

// cells reads a cell-count property (#address-cells/#size-cells), or def.
func (n *Node) cells(name string, def int) int {
	if v, ok := n.props[name]; ok && len(v) >= 4 {
		return int(be32(v, 0))
	}
	return def
}

// be32 reads a big-endian uint32 at b[off:]; an out-of-range off panics and is
// recovered by Parse as an invalid blob.
func be32(b []byte, off int) uint32 {
	return uint32(b[off])<<24 | uint32(b[off+1])<<16 | uint32(b[off+2])<<8 | uint32(b[off+3])
}
