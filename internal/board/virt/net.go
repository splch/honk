//go:build tamago && riscv64

package virt

import (
	"time"

	"github.com/splch/honk/internal/inet"
	"github.com/splch/honk/internal/virtio"
)

// QEMU user-mode networking (-netdev user) defaults: the guest is 10.0.2.15 and
// the gateway/NAT is 10.0.2.2, which answers ARP and ICMP. honk uses static
// addressing (no DHCP).
var (
	hostIP    = inet.IP{10, 0, 2, 15}
	gatewayIP = inet.IP{10, 0, 2, 2}
	net0      *virtio.Net
	pingID    uint16 = 0x484b // "HK"
)

// initNet finds and initializes a virtio-net device, logging its MAC. Called
// from hwinit1 after paging maps the virtio MMIO.
func initNet() {
	for i := uintptr(0); i < 8; i++ {
		base := uintptr(virtioBase) + i*0x1000
		if !virtio.IsNet(base) {
			continue
		}
		n, err := virtio.NewNet(base)
		if err != nil {
			puts("honk/virt: virtio-net init failed: ")
			puts(err.Error())
			puts("\n")
			return
		}
		net0 = n
		break
	}
	if net0 == nil {
		return
	}
	puts("honk/virt: net up, MAC ")
	printMAC(net0.MAC())
	puts(", IP 10.0.2.15\n")
}

// netCmd is the `net` shell command: it ARPs for the gateway and pings it,
// exercising virtio-net transmit and receive end to end.
func netCmd() {
	if net0 == nil {
		puts("no net device\r\n")
		return
	}
	net0.Send(inet.ARPRequest(net0.MAC(), hostIP, gatewayIP))
	gwMAC, ok := awaitARP(gatewayIP)
	if !ok {
		puts("net: no ARP reply from gateway\r\n")
		return
	}
	puts("net: gateway 10.0.2.2 is at ")
	printMAC(gwMAC)
	puts("\r\n")

	net0.Send(inet.ICMPEcho(net0.MAC(), gwMAC, hostIP, gatewayIP, pingID, 1, []byte("honk")))
	if awaitPing(pingID) {
		puts("net: ping 10.0.2.2: reply received\r\n")
	} else {
		puts("net: ping 10.0.2.2: no reply\r\n")
	}
}

// awaitARP polls received frames for an ARP reply from ip (~200 ms timeout).
func awaitARP(ip inet.IP) (inet.MAC, bool) {
	for try := 0; try < 200; try++ {
		f, ok := net0.Recv()
		if !ok {
			time.Sleep(time.Millisecond)
			continue
		}
		if mac, sip, ok := inet.ParseARPReply(f); ok && sip == ip {
			return mac, true
		}
	}
	return inet.MAC{}, false
}

// awaitPing polls received frames for an ICMP echo reply with the given id.
func awaitPing(id uint16) bool {
	for try := 0; try < 200; try++ {
		f, ok := net0.Recv()
		if !ok {
			time.Sleep(time.Millisecond)
			continue
		}
		if rid, _, ok := inet.ParseICMPEchoReply(f); ok && rid == id {
			return true
		}
	}
	return false
}

func printMAC(m inet.MAC) {
	const hex = "0123456789abcdef"
	for i, b := range m {
		if i > 0 {
			uart0.Tx(':')
		}
		uart0.Tx(hex[b>>4])
		uart0.Tx(hex[b&0xf])
	}
}
