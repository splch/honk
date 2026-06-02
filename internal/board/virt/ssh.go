//go:build tamago && riscv64

package virt

import (
	"crypto/ed25519"
	crand "crypto/rand"
	"io"
	"strings"

	"github.com/gliderlabs/ssh"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// serveSSH runs an SSH server exposing the honk shell over the gVisor stack —
// the daily-driver remote login (DESIGN.md §15.1, step 4). The ed25519 host key
// is generated fresh from crypto/rand, which is safe now that the virtio-rng
// entropy source is installed (step 1). Authentication is open for the demo;
// real deployments should set a PublicKeyHandler with authorized keys.
func serveSSH() {
	_, priv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		puts("honk/virt: ssh keygen failed: ")
		puts(err.Error())
		puts("\n")
		return
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		puts("honk/virt: ssh signer failed: ")
		puts(err.Error())
		puts("\n")
		return
	}

	srv := &ssh.Server{Addr: ":22", Handler: sshSession}
	srv.AddHostKey(signer)

	puts("honk/virt: SSH on :22 (host-forwarded)\n")
	if err := srv.ListenAndServe(); err != nil {
		puts("honk/virt: ssh server exited: ")
		puts(err.Error())
		puts("\n")
	}
}

// sshSession runs the honk shell for one connection. `ssh host <cmd>` runs the
// command once (exec mode); a bare `ssh host` serves the interactive prompt via
// golang.org/x/term, sharing runCmd with the local UART console.
func sshSession(s ssh.Session) {
	if cmd := s.Command(); len(cmd) > 0 {
		runCmd(s, strings.Join(cmd, " "))
		return
	}

	t := term.NewTerminal(s, "honk> ")
	io.WriteString(t, "honk over SSH. type 'help'; 'exit' to quit.\r\n")
	shellLoop(t, true) // shared line editor + dispatch with the UART console
}
