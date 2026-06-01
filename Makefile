# Honk OS — a small educational operating system written in pure Go for RISC-V.
#
# Quick start:
#   make toolchain   # one-time: build the patched Embedded Go toolchain (~5 min)
#   make run         # build the kernel and boot it in QEMU (Ctrl-A x to quit)
#   make test        # non-interactive smoke test (pipes commands, expects poweroff)
#   make debug       # boot paused for gdb on :1234
#
# Requirements: an existing Go (>=1.22) as bootstrap, git, and qemu-system-riscv64.
# Override the toolchain with e.g.  make GO=/path/to/embeddedgo/bin/go run

GO        ?= $(CURDIR)/.toolchain/go/bin/go
KERNEL    := honk.elf
LOAD_ADDR := 0x80200000
RAM_SIZE  := 30M

GOENV := GOOS=noos GOARCH=riscv64 CGO_ENABLED=0
BUILD := $(GOENV) $(GO) build -tags noostest -ldflags '-M $(LOAD_ADDR):$(RAM_SIZE)'
QEMU  := qemu-system-riscv64 -machine virt -m 32 -smp 1 -bios default

.PHONY: all kernel run test debug toolchain clean distclean

all: kernel

kernel: | $(GO)
	$(BUILD) -o $(KERNEL) ./cmd/honk

run: kernel
	$(QEMU) -nographic -kernel $(KERNEL)

test: kernel
	@printf '\nhelp\nhonk\nuname\nstats\nmem\ngc\nmem\nhalt\n' | \
		$(QEMU) -display none -serial stdio -no-reboot -kernel $(KERNEL)

debug: kernel
	@echo "QEMU paused; connect: gdb $(KERNEL) -ex 'target remote :1234'"
	$(QEMU) -nographic -kernel $(KERNEL) -s -S

# Build the patched Embedded Go toolchain into ./.toolchain/go (one-time).
toolchain $(GO):
	./toolchain/build-toolchain.sh

clean:
	rm -f $(KERNEL)

distclean: clean
	rm -rf .toolchain
