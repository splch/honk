// Package inet is a tiny, hardware-independent IPv4 stack: it builds and parses
// the Ethernet / ARP / IPv4 / ICMP frames honk needs to ARP for and ping a
// gateway. It is pure Go with no device dependency, so the wire-format logic
// (layout, checksums) is unit-tested on the host (GO.md §16); a driver supplies
// the actual frames.
package inet

import "encoding/binary"

// MAC is an Ethernet hardware address; IP is an IPv4 address.
type MAC [6]byte
type IP [4]byte

var (
	Broadcast = MAC{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	zeroMAC   = MAC{}
)

// EtherTypes and protocol numbers.
const (
	etherARP  = 0x0806
	etherIPv4 = 0x0800
	protoICMP = 1

	arpRequest = 1
	arpReply   = 2

	icmpEchoReply   = 0
	icmpEchoRequest = 8

	ethHdrLen = 14
	ipHdrLen  = 20
)

// frame prepends an Ethernet header to payload.
func frame(dst, src MAC, etherType uint16, payload []byte) []byte {
	b := make([]byte, ethHdrLen+len(payload))
	copy(b[0:6], dst[:])
	copy(b[6:12], src[:])
	binary.BigEndian.PutUint16(b[12:14], etherType)
	copy(b[ethHdrLen:], payload)
	return b
}

// ParseEther splits an Ethernet frame into its addresses, type, and payload.
func ParseEther(b []byte) (dst, src MAC, etherType uint16, payload []byte, ok bool) {
	if len(b) < ethHdrLen {
		return
	}
	copy(dst[:], b[0:6])
	copy(src[:], b[6:12])
	etherType = binary.BigEndian.Uint16(b[12:14])
	return dst, src, etherType, b[ethHdrLen:], true
}

// ARPRequest builds a broadcast "who has targetIP?" Ethernet frame.
func ARPRequest(src MAC, srcIP, targetIP IP) []byte {
	p := make([]byte, 28)
	binary.BigEndian.PutUint16(p[0:2], 1)         // hardware type: Ethernet
	binary.BigEndian.PutUint16(p[2:4], etherIPv4) // protocol type: IPv4
	p[4] = 6                                      // hardware size
	p[5] = 4                                      // protocol size
	binary.BigEndian.PutUint16(p[6:8], arpRequest)
	copy(p[8:14], src[:])
	copy(p[14:18], srcIP[:])
	// target hardware addr left zero
	copy(p[24:28], targetIP[:])
	return frame(Broadcast, src, etherARP, p)
}

// ParseARPReply extracts the sender's MAC/IP from an ARP reply frame.
func ParseARPReply(b []byte) (mac MAC, ip IP, ok bool) {
	_, _, et, p, ok := ParseEther(b)
	if !ok || et != etherARP || len(p) < 28 {
		return mac, ip, false
	}
	if binary.BigEndian.Uint16(p[6:8]) != arpReply {
		return mac, ip, false
	}
	copy(mac[:], p[8:14])
	copy(ip[:], p[14:18])
	return mac, ip, true
}

// checksum computes the 16-bit one's-complement Internet checksum (RFC 1071).
func checksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

// ICMPEcho builds an ICMP echo-request Ethernet frame to dstIP/dstMAC.
func ICMPEcho(srcMAC, dstMAC MAC, srcIP, dstIP IP, id, seq uint16, data []byte) []byte {
	icmp := make([]byte, 8+len(data))
	icmp[0] = icmpEchoRequest
	binary.BigEndian.PutUint16(icmp[4:6], id)
	binary.BigEndian.PutUint16(icmp[6:8], seq)
	copy(icmp[8:], data)
	binary.BigEndian.PutUint16(icmp[2:4], checksum(icmp))

	ip := make([]byte, ipHdrLen+len(icmp))
	ip[0] = 0x45 // version 4, IHL 5
	binary.BigEndian.PutUint16(ip[2:4], uint16(len(ip)))
	ip[8] = 64 // TTL
	ip[9] = protoICMP
	copy(ip[12:16], srcIP[:])
	copy(ip[16:20], dstIP[:])
	binary.BigEndian.PutUint16(ip[10:12], checksum(ip[:ipHdrLen]))
	copy(ip[ipHdrLen:], icmp)

	return frame(dstMAC, srcMAC, etherIPv4, ip)
}

// ParseICMPEchoReply reports whether b is an ICMP echo reply with id/seq.
func ParseICMPEchoReply(b []byte) (id, seq uint16, ok bool) {
	_, _, et, p, ok := ParseEther(b)
	if !ok || et != etherIPv4 || len(p) < ipHdrLen {
		return 0, 0, false
	}
	ihl := int(p[0]&0x0f) * 4
	if p[9] != protoICMP || len(p) < ihl+8 {
		return 0, 0, false
	}
	icmp := p[ihl:]
	if icmp[0] != icmpEchoReply {
		return 0, 0, false
	}
	return binary.BigEndian.Uint16(icmp[4:6]), binary.BigEndian.Uint16(icmp[6:8]), true
}
