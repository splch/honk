// Package console provides line-oriented terminal I/O over a byte device, with
// echo and basic line editing. It is deliberately transport-agnostic (a UART
// today; a virtio-console or framebuffer later) via the Device interface.
package console

// Device is the byte-level transport a Console drives.
type Device interface {
	Putc(c byte) // transmit one byte
	Getc() byte  // block for and return one byte
}

// Console is a simple line-editing terminal over a Device.
type Console struct{ dev Device }

// New returns a Console over dev.
func New(dev Device) *Console { return &Console{dev: dev} }

// WriteString writes s, translating '\n' to CR+LF for terminal output.
func (c *Console) WriteString(s string) {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			c.dev.Putc('\r')
		}
		c.dev.Putc(s[i])
	}
}

// ReadLine reads one line, echoing input and handling backspace/DEL. The
// returned string excludes the line terminator.
func (c *Console) ReadLine() string {
	var buf []byte
	for {
		switch ch := c.dev.Getc(); ch {
		case '\r', '\n':
			c.WriteString("\n")
			return string(buf)
		case 0x08, 0x7f: // backspace / DEL
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
				c.WriteString("\b \b")
			}
		default:
			if ch >= 0x20 && ch < 0x7f {
				buf = append(buf, ch)
				c.dev.Putc(ch) // echo
			}
		}
	}
}
