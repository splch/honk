package console

import "testing"

// These tests run on the host with `go test ./kernel/console` — no QEMU, no
// hardware. fakeDev stands in for a UART: it returns scripted input from Getc
// and records everything written via Putc, so we can assert both the returned
// line and the exact bytes echoed back to the terminal.

type fakeDev struct {
	in  []byte // bytes Getc hands back, in order
	pos int
	out []byte // bytes written via Putc
}

func (f *fakeDev) Getc() byte {
	c := f.in[f.pos]
	f.pos++
	return c
}

func (f *fakeDev) Putc(c byte) { f.out = append(f.out, c) }

func TestWriteStringTranslatesLF(t *testing.T) {
	f := &fakeDev{}
	New(f).WriteString("a\nb")
	if got := string(f.out); got != "a\r\nb" {
		t.Errorf("WriteString output = %q, want %q (\\n must become CR+LF)", got, "a\r\nb")
	}
}

func TestReadLineEchoAndTerminator(t *testing.T) {
	// Each printable byte is echoed; the CR terminator is echoed as CR+LF and
	// excluded from the result.
	f := &fakeDev{in: []byte("hi\r")}
	if got := New(f).ReadLine(); got != "hi" {
		t.Errorf("ReadLine = %q, want %q", got, "hi")
	}
	if got := string(f.out); got != "hi\r\n" {
		t.Errorf("echo = %q, want %q", got, "hi\r\n")
	}
}

func TestReadLineLF(t *testing.T) {
	// A bare LF terminates the line just like CR.
	f := &fakeDev{in: []byte("x\n")}
	if got := New(f).ReadLine(); got != "x" {
		t.Errorf("ReadLine = %q, want %q", got, "x")
	}
}

func TestReadLineBackspace(t *testing.T) {
	// Both BS (0x08) and DEL (0x7f) erase the last byte and emit "\b \b".
	for _, bs := range []byte{0x08, 0x7f} {
		f := &fakeDev{in: []byte{'a', 'b', bs, 'c', '\r'}}
		if got := New(f).ReadLine(); got != "ac" {
			t.Errorf("ReadLine with erase %#x = %q, want %q", bs, got, "ac")
		}
		if got := string(f.out); got != "ab\b \bc\r\n" {
			t.Errorf("echo with erase %#x = %q, want %q", bs, got, "ab\b \bc\r\n")
		}
	}
}

func TestReadLineBackspaceOnEmptyIsNoop(t *testing.T) {
	// Erasing an empty buffer must do nothing — no underflow, no echo.
	f := &fakeDev{in: []byte{0x7f, 'x', '\r'}}
	if got := New(f).ReadLine(); got != "x" {
		t.Errorf("ReadLine = %q, want %q", got, "x")
	}
	if got := string(f.out); got != "x\r\n" {
		t.Errorf("echo = %q, want %q (erase on empty must be silent)", got, "x\r\n")
	}
}

func TestReadLineIgnoresControlBytes(t *testing.T) {
	// Non-printable bytes (other than erase/terminator) are dropped, not echoed.
	f := &fakeDev{in: []byte{'a', 0x01, 0x1b, 'b', '\n'}}
	if got := New(f).ReadLine(); got != "ab" {
		t.Errorf("ReadLine = %q, want %q", got, "ab")
	}
	if got := string(f.out); got != "ab\r\n" {
		t.Errorf("echo = %q, want %q", got, "ab\r\n")
	}
}
