package inet

import "testing"

func TestChecksum(t *testing.T) {
	// A correct IP/ICMP checksum makes the sum over the block come to zero
	// (the stored field plus the rest is the one's-complement of itself).
	got := ICMPEcho(MAC{1, 2, 3, 4, 5, 6}, MAC{6, 5, 4, 3, 2, 1},
		IP{10, 0, 2, 15}, IP{10, 0, 2, 2}, 0xbeef, 7, []byte("honk-ping"))
	_, _, et, ip, ok := ParseEther(got)
	if !ok || et != etherIPv4 {
		t.Fatalf("not an IPv4 frame: ok=%v et=%#x", ok, et)
	}
	if c := checksum(ip[:ipHdrLen]); c != 0 {
		t.Errorf("IP header checksum verify = %#x, want 0", c)
	}
	if c := checksum(ip[ipHdrLen:]); c != 0 {
		t.Errorf("ICMP checksum verify = %#x, want 0", c)
	}
}

func TestARPRoundTrip(t *testing.T) {
	src := MAC{0x52, 0x54, 0x00, 0x12, 0x34, 0x56}
	req := ARPRequest(src, IP{10, 0, 2, 15}, IP{10, 0, 2, 2})
	dst, gotSrc, et, _, ok := ParseEther(req)
	if !ok || et != etherARP || dst != Broadcast || gotSrc != src {
		t.Fatalf("bad ARP request framing: ok=%v et=%#x dst=%v", ok, et, dst)
	}
	// A request is not a reply.
	if _, _, ok := ParseARPReply(req); ok {
		t.Error("ParseARPReply accepted a request")
	}
	// Hand-craft a reply and parse it.
	reply := make([]byte, 28)
	copy(reply[6:8], []byte{0, arpReply})
	gwMAC := MAC{0x52, 0x55, 0x0a, 0x00, 0x02, 0x02}
	copy(reply[8:14], gwMAC[:])
	copy(reply[14:18], []byte{10, 0, 2, 2})
	mac, ip, ok := ParseARPReply(frame(src, gwMAC, etherARP, reply))
	if !ok || mac != gwMAC || ip != (IP{10, 0, 2, 2}) {
		t.Fatalf("ParseARPReply = %v, %v, %v", mac, ip, ok)
	}
}

func TestICMPEchoRoundTrip(t *testing.T) {
	// An echo request reflected back as a reply (type 0) must parse with the
	// same id/seq.
	req := ICMPEcho(MAC{1, 1, 1, 1, 1, 1}, MAC{2, 2, 2, 2, 2, 2},
		IP{10, 0, 2, 15}, IP{10, 0, 2, 2}, 0x1234, 9, []byte("x"))
	_, _, _, ip, _ := ParseEther(req)
	ihl := int(ip[0]&0x0f) * 4
	ip[ihl] = icmpEchoReply // flip request -> reply
	if _, _, ok := ParseICMPEchoReply(req); !ok {
		t.Fatal("ParseICMPEchoReply rejected a reply")
	}
	id, seq, ok := ParseICMPEchoReply(req)
	if !ok || id != 0x1234 || seq != 9 {
		t.Errorf("ParseICMPEchoReply = %#x, %d, %v; want 0x1234, 9, true", id, seq, ok)
	}
}
