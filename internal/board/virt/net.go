//go:build tamago && riscv64

package virt

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/splch/honk/internal/virtio"
)

// QEMU user-mode networking (-netdev user) defaults: the guest is 10.0.2.15 and
// the gateway/NAT is 10.0.2.2. honk uses static addressing (no DHCP).
var net0 *virtio.Net

// initNet finds and initializes a virtio-net device, logging its MAC. Called
// from hwinit1 after paging maps the virtio MMIO. The gVisor TCP/IP stack is
// then brought up over it from a package init() (netstack.go).
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
	mac := net0.MAC()
	puts("honk/virt: net up, MAC ")
	puts(net.HardwareAddr(mac[:]).String())
	puts(", IP 10.0.2.15\n")
}

// netCmd is the `net` shell command. It reports the interface and proves the
// whole NIC → gVisor → UDP → DNS path by resolving a name through the stack
// honk already runs (netstack.go). The old hand-rolled ARP/ICMP ping is gone:
// gVisor handles ARP/IPv4/ICMP, and reading frames directly here would race the
// stack's own rxLoop for the NIC.
func netCmd(w io.Writer) {
	if net0 == nil {
		io.WriteString(w, "no net device\r\n")
		return
	}
	mac := net0.MAC()
	fmt.Fprintf(w, "net: MAC %s, IP 10.0.2.15/24, gateway 10.0.2.2\r\n", net.HardwareAddr(mac[:]))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(ctx, "example.com")
	if err != nil {
		fmt.Fprintf(w, "net: DNS lookup failed: %v\r\n", err)
		return
	}
	fmt.Fprintf(w, "net: resolved example.com -> %v (NIC+gVisor+DNS ok)\r\n", addrs)
}
