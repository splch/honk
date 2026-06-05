//go:build tamago && riscv64

package virt

import (
	"crypto/ed25519"
	crand "crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"strings"
	"sync"
	"time"

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
// SSH resource bounds. honk runs on a single hart with one address space, so an
// unauthenticated flood must not be able to exhaust goroutines/memory.
const (
	maxSSHConns = 8
	sshIdle     = 5 * time.Minute  // close idle connections
	sshMax      = 60 * time.Minute // absolute per-connection cap
)

var sshConnSem = make(chan struct{}, maxSSHConns)

func serveSSH() {
	// Fail closed: never expose an unauthenticated shell on a single-address-space
	// unikernel. The SSH server starts only when an 'authkeys' file of authorized
	// public keys exists; bootstrap one from the local UART console first:
	//   write authkeys ssh-ed25519 AAAA...
	keys := authorizedKeys()
	if len(keys) == 0 {
		puts("honk/virt: SSH disabled — no 'authkeys' (add a public key via the console to enable)\n")
		return
	}

	signer, err := hostKeySigner()
	if err != nil {
		puts("honk/virt: ssh keygen failed: ")
		puts(err.Error())
		puts("\n")
		return
	}

	srv := &ssh.Server{
		Addr:                 ":22",
		Handler:              sshSession,
		IdleTimeout:          sshIdle,
		MaxTimeout:           sshMax,
		ConnCallback:         limitConns,
		ServerConfigCallback: modernSSHConfig,
		PublicKeyHandler: func(_ ssh.Context, key ssh.PublicKey) bool {
			for _, ak := range keys {
				if ssh.KeysEqual(ak, key) { // constant-time compare
					return true
				}
			}
			return false
		},
	}
	srv.AddHostKey(signer)
	puts("honk/virt: SSH on :22, public-key auth (authkeys)\n")

	if err := srv.ListenAndServe(); err != nil {
		puts("honk/virt: ssh server exited: ")
		puts(err.Error())
		puts("\n")
	}
}

// limitConns caps concurrent SSH connections: over the limit, returning nil
// makes gliderlabs close the connection immediately, bounding pre-auth resource
// use. The slot is released when the wrapped connection closes.
func limitConns(_ ssh.Context, conn net.Conn) net.Conn {
	select {
	case sshConnSem <- struct{}{}:
		return &limitedConn{Conn: conn}
	default:
		return nil
	}
}

type limitedConn struct {
	net.Conn
	once sync.Once
}

func (c *limitedConn) Close() error {
	c.once.Do(func() { <-sshConnSem })
	return c.Conn.Close()
}

// modernSSHConfig pins key-exchange, cipher, and MAC algorithms to modern
// AEAD/ETM choices, dropping the SHA-1 / CBC / ssh-rsa options x/crypto still
// offers by default. gliderlabs layers honk's host key and public-key auth onto
// the returned config (see server.config).
func modernSSHConfig(ssh.Context) *gossh.ServerConfig {
	return &gossh.ServerConfig{
		Config: gossh.Config{
			KeyExchanges: []string{gossh.KeyExchangeMLKEM768X25519, gossh.KeyExchangeCurve25519},
			Ciphers:      []string{gossh.CipherChaCha20Poly1305, gossh.CipherAES256GCM, gossh.CipherAES128GCM},
			MACs:         []string{gossh.HMACSHA256ETM, gossh.HMACSHA512ETM},
		},
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
			if werr := WriteFile(hostKeyFile, pem.EncodeToMemory(blk)); werr != nil {
				puts("honk/virt: WARNING could not persist SSH host key: ")
				puts(werr.Error())
				puts("\n")
			}
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
