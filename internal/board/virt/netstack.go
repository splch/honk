//go:build tamago && riscv64

package virt

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"time"

	"github.com/splch/honk/internal/virtio"
	gnet "github.com/usbarmory/go-net"

	// Embeds the Mozilla CA roots and calls crypto/x509.SetFallbackRoots in its
	// init, so crypto/tls can verify servers without an OS trust store (step 3).
	_ "golang.org/x/crypto/x509roots/fallback"
)

// dnsServer is QEMU SLIRP's built-in resolver; honk has no /etc/resolv.conf, so
// it points the Go resolver here explicitly (DESIGN.md §15.5).
const dnsServer = "10.0.2.3:53"

var booted = time.Now()

// netDevice adapts honk's virtio.Net to go-net's 2-method NetworkDevice, the
// only seam gVisor's netstack needs (DESIGN.md §15.3).
type netDevice struct{ n *virtio.Net }

func (d netDevice) Receive(buf []byte) (int, error) {
	f, ok := d.n.Recv()
	if !ok {
		return 0, nil
	}
	return copy(buf, f), nil
}

func (d netDevice) Transmit(buf []byte) error {
	d.n.Send(buf)
	return nil
}

var iface *gnet.Interface

// init brings up the network stack. It runs as a package init() (not from
// hwinit1) so that gVisor's own package initialization has completed first;
// hwinit1 has already set up entropy, the console, and the NIC by this point.
func init() { initStack() }

// initStack brings up the gVisor TCP/IP stack over the virtio-net device and
// routes the entire stdlib net surface (net/http, crypto/tls, DNS, SSH) through
// it by setting net.SocketFunc. SLIRP user-net addressing (DESIGN.md §15.3).
func initStack() {
	if net0 == nil {
		return
	}
	mac := net0.MAC()
	iface = &gnet.Interface{NetworkDevice: netDevice{net0}}
	if err := iface.Init("10.0.2.15/24", net.HardwareAddr(mac[:]).String(), "10.0.2.2"); err != nil {
		puts("honk/virt: netstack init failed: ")
		puts(err.Error())
		puts("\n")
		return
	}
	if err := iface.Stack.EnableICMP(); err != nil {
		puts("honk/virt: EnableICMP failed: ")
		puts(err.Error())
		puts("\n")
	}
	net.SocketFunc = iface.Stack.Socket // stdlib net now flows through gVisor
	// Resolve DNS via SLIRP's server (no resolv.conf exists on a unikernel).
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, dnsServer)
		},
	}
	go rxLoop() // drain inbound frames; sleeps (wfi) when the NIC is idle
	puts("honk/virt: TCP/IP up (gVisor netstack), 10.0.2.15/24\n")
	go serveHTTP()
	go serveSSH()
}

// rxLoop's poll backs off between these bounds. honk has no resumable ISR
// (sstatus.SIE = 0), so it cannot run the receive path from an interrupt;
// instead rxLoop drains every available frame, then sleeps only when the device
// is empty — which lets the scheduler reach idle and the hart enter wfi
// (DESIGN.md §15.7). The interval starts tight, so active SSH/HTTP exchanges see
// sub-millisecond latency and bursts never sleep, and doubles toward rxPollMax
// as the link stays quiet, so an idle appliance wakes only ~100×/s.
const (
	rxPollMin = 250 * time.Microsecond
	rxPollMax = 10 * time.Millisecond
)

// rxLoop pumps inbound Ethernet frames from the NIC into the gVisor stack,
// replacing go-net's Interface.Start whose `for { rx; runtime.Gosched() }` is
// always runnable and pegs the hart. It uses only go-net's exported seam (the
// NetworkDevice and Stack fields), making it the interrupt-free analogue of the
// console's drain-then-sleep model. Transmit is already event-driven via the
// stack's write-notify callback (wired in Interface.Init), so only receive
// needs a pump.
func rxLoop() {
	buf := make([]byte, gnet.MTU+gnet.EthernetMaximumSize)
	delay := rxPollMin
	for {
		if n, err := iface.NetworkDevice.Receive(buf); err == nil && n > 0 {
			_ = iface.Stack.RecvInboundPacket(buf[:n])
			delay = rxPollMin // active: return to tight polling
			continue
		}
		time.Sleep(delay)
		if delay < rxPollMax {
			delay *= 2
		}
	}
}

// serveHTTP runs a tiny stdlib net/http status server over the gVisor stack,
// proving the full TCP path. Reachable from the host via the Makefile's
// hostfwd (curl http://127.0.0.1:8080/).
func serveHTTP() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "honk! a pure-Go RISC-V 64-bit unikernel on TamaGo.\n")
		fmt.Fprintf(w, "uptime %s, %d goroutines, served over gVisor netstack.\n",
			time.Since(booted).Round(time.Second), runtime.NumGoroutine())
	})
	http.ListenAndServe(":80", mux)
}

// fetchURL performs an outbound HTTP(S) GET and writes a short summary to w,
// exercising DNS, TCP, and (for https) TLS with the fallback roots and the
// build/NTP wall clock. Backs the shell `fetch <url>` command.
func fetchURL(w io.Writer, url string) {
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		fmt.Fprintf(w, "fetch: %v\r\n", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	fmt.Fprintf(w, "%s  (%d bytes shown)\r\n", resp.Status, len(body))
	w.Write(body)
	io.WriteString(w, "\r\n")
}
