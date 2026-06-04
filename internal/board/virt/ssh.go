//go:build tamago && riscv64

package virt

import (
	"crypto/ed25519"
	crand "crypto/rand"
	"encoding/pem"
	"io"
	"strings"

	"github.com/gliderlabs/ssh"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// File names on the FAT32 disk (kept short for 8.3 compatibility).
const (
	hostKeyFile  = "sshkey"   // persisted ed25519 host key (OpenSSH PEM)
	authKeysFile = "authkeys" // optional authorized public keys, one per line
)

// serveSSH runs an SSH server exposing the honk shell over the gVisor stack —
// the daily-driver remote login (DESIGN.md §15.1, step 4). The ed25519 host key
// is loaded from (or generated and persisted to) the disk so a client's known
// hosts entry stays valid across reboots. Authentication requires a public key
// when an authorized_keys file is present, and is otherwise open (demo).
func serveSSH() {
	signer, err := hostKeySigner()
	if err != nil {
		puts("honk/virt: ssh keygen failed: ")
		puts(err.Error())
		puts("\n")
		return
	}

	srv := &ssh.Server{Addr: ":22", Handler: sshSession}
	srv.AddHostKey(signer)

	if keys := authorizedKeys(); len(keys) > 0 {
		srv.PublicKeyHandler = func(_ ssh.Context, key ssh.PublicKey) bool {
			for _, ak := range keys {
				if ssh.KeysEqual(ak, key) {
					return true
				}
			}
			return false
		}
		puts("honk/virt: SSH on :22, public-key auth (authkeys)\n")
	} else {
		puts("honk/virt: SSH on :22, auth OPEN (add 'authkeys' to require public keys)\n")
	}

	if err := srv.ListenAndServe(); err != nil {
		puts("honk/virt: ssh server exited: ")
		puts(err.Error())
		puts("\n")
	}
}

// hostKeySigner returns the SSH host key, loading the persisted one from disk if
// present and otherwise generating a fresh ed25519 key (safe now that virtio-rng
// entropy is installed) and writing it back so it is stable across reboots.
func hostKeySigner() (gossh.Signer, error) {
	if FS != nil {
		if data, err := ReadFile(hostKeyFile); err == nil && len(data) > 0 {
			if s, err := gossh.ParsePrivateKey(data); err == nil {
				return s, nil
			}
		}
	}
	_, priv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		return nil, err
	}
	if FS != nil {
		if blk, err := gossh.MarshalPrivateKey(priv, "honk"); err == nil {
			_ = WriteFile(hostKeyFile, pem.EncodeToMemory(blk)) // best-effort persist
		}
	}
	return gossh.NewSignerFromKey(priv)
}

// authorizedKeys reads the optional authorized-keys file from the disk; when it
// exists the SSH server requires one of these public keys to authenticate.
func authorizedKeys() []ssh.PublicKey {
	if FS == nil {
		return nil
	}
	data, err := ReadFile(authKeysFile)
	if err != nil || len(data) == 0 {
		return nil
	}
	var keys []ssh.PublicKey
	for len(data) > 0 {
		k, _, _, rest, err := gossh.ParseAuthorizedKey(data)
		if err != nil {
			break
		}
		keys = append(keys, k)
		data = rest
	}
	return keys
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
