// honk - networking (M6): virtio-net + the gVisor TCP/IP stack (go-net),
// wired into the stdlib net package via net.SocketFunc (HONK.md §1: TCP/IP is
// reused, not written; stdlib net is the interface).
//
// honk drives a virtio-net device (board/virt) as a go-net NetworkDevice, runs
// gVisor as the stack, and points net.SocketFunc at it so net/http, crypto/tls,
// and the rest of the stdlib net package work unchanged. QEMU user-mode
// networking (SLIRP) gives the guest a fixed address, so honk configures it
// statically (no DHCP client needed for the appliance).

//go:build tamago && riscv64

package main

import (
	"fmt"
	"net"
	"net/http"
	"time"

	gnet "github.com/usbarmory/go-net"

	"honk/board/virt"
)

// QEMU user-mode (SLIRP) network: the guest address, gateway, and DNS are
// fixed and well-known, so honk uses them statically.
const (
	netCIDR    = "10.0.2.15/24"
	netGateway = "10.0.2.2"
)

var (
	netStack *gnet.GVisorStack // the live stack (nil if no NIC), for the shell
	netMAC   string            // the NIC MAC, for the shell
)

// InitNet brings up networking: it discovers the virtio-net device, starts the
// gVisor stack over it, registers net.SocketFunc so the stdlib net package is
// live, and starts an HTTP status server. With no device it is a no-op (honk
// runs fine without a network, e.g. under Phase A).
func InitNet() {
	dev := virt.ProbeNet()
	if dev == nil {
		fmt.Println("honk: no network device")
		return
	}

	mac := ""
	if m := dev.MAC(); m != nil {
		mac = net.HardwareAddr(m).String()
	}

	stk := gnet.NewGVisorStack(1)
	iface := &gnet.Interface{Stack: stk, NetworkDevice: dev}
	if err := iface.Init(netCIDR, mac, netGateway); err != nil {
		fmt.Printf("honk: net init failed: %v\n", err)
		return
	}

	// Light up the stdlib net package: every net.Dial/Listen now routes through
	// the gVisor stack. EnableICMP makes the interface answer pings.
	net.SocketFunc = stk.Socket
	_ = stk.EnableICMP()

	netStack = stk
	if hw, err := stk.HardwareAddress(); err == nil {
		netMAC = hw.String()
	}

	go iface.Start() // the receive pump: NetworkDevice.Receive -> stack

	fmt.Printf("honk: net up  ip=%s  gw=%s  mac=%s\n", netCIDR, netGateway, netMAC)
	serveHTTP(stk)
}

// netcmd is the shell's `net` command: it reports the interface status.
func netcmd() {
	if netStack == nil {
		fmt.Println("net: no network device")
		return
	}
	fmt.Printf("net: ip=%s  gw=%s  mac=%s  (httpd on :80)\n", netCIDR, netGateway, netMAC)
}

// serveHTTP starts honk's HTTP status server on port 80, proving the whole
// chain (virtio-net -> gVisor -> stdlib net.Listener -> net/http) end to end.
func serveHTTP(stk *gnet.GVisorStack) {
	ln, err := stk.ListenerTCP4(80)
	if err != nil {
		fmt.Printf("honk: httpd listen failed: %v\n", err)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "honk - pure-Go RISC-V64 OS\nuptime %s\nharts %d\n",
			virt.Uptime().Round(time.Second), virt.NumHarts())
	})
	go func() {
		if err := http.Serve(ln, mux); err != nil {
			fmt.Printf("honk: httpd stopped: %v\n", err)
		}
	}()
	fmt.Println("honk: httpd serving on :80")
}
