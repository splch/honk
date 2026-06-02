# Honk OS — a small educational operating system written in pure Go for RISC-V.
#
# Quick start:
#   make toolchain      # one-time: build the patched Embedded Go toolchain (~5 min)
#   make run            # build the kernel and boot it in QEMU (Ctrl-A x to quit)
#   make test           # non-interactive smoke test (pipes commands, expects poweroff)
#   make test-discover  # boot with a non-default RAM size; proves device-tree discovery
#   make debug          # boot paused for gdb on :1234
#
# Requirements: an existing Go (>=1.22) as bootstrap, git, and qemu-system-riscv64.
# Override the toolchain with e.g.  make GO=/path/to/embeddedgo/bin/go run

GO        ?= $(CURDIR)/.toolchain/go/bin/go
KERNEL    := honk.elf
LOAD_ADDR := 0x80200000
RAM_SIZE  := 30M

GOENV := GOOS=noos GOARCH=riscv64 CGO_ENABLED=0
BUILD := $(GOENV) $(GO) build -tags noostest -ldflags '-M $(LOAD_ADDR):$(RAM_SIZE)'

# QEMU RAM in MiB. Deferred (=) so test-discover can override it per-target; the
# 'devices' command then reports this size only when device-tree discovery works.
QEMU_MEM ?= 32
QEMU = qemu-system-riscv64 -machine virt -m $(QEMU_MEM) -smp 1 -bios default

WEB_OUT := site

.PHONY: all kernel run test test-discover debug toolchain clean distclean web web-qemu web-serve

all: kernel

kernel: | $(GO)
	$(BUILD) -o $(KERNEL) ./cmd/honk

run: kernel
	$(QEMU) -nographic -kernel $(KERNEL)

test: kernel
	@printf '\nhelp\nhonk\nuname\ndevices\nstats\nmem\ngc\nmem\nhalt\n' | \
		$(QEMU) -display none -serial stdio -no-reboot -kernel $(KERNEL)

# Boot with a non-default RAM size and exercise 'devices'. With device-tree
# discovery working the reported size tracks -m (64MiB); the no-discovery
# fallback would report the 32MiB default. CI asserts on the difference, which
# is what proves the boot stub captured the firmware's device tree pointer (a1).
test-discover: QEMU_MEM := 64
test-discover: kernel
	@printf '\ndevices\nhalt\n' | \
		$(QEMU) -display none -serial stdio -no-reboot -kernel $(KERNEL)

debug: kernel
	@echo "QEMU paused; connect: gdb $(KERNEL) -ex 'target remote :1234'"
	$(QEMU) -nographic -kernel $(KERNEL) -s -S

# Build the patched Embedded Go toolchain into ./.toolchain/go (one-time).
toolchain $(GO):
	./toolchain/build-toolchain.sh

# Assemble the static site for GitHub Pages into ./site. Bundles the recorded boot
# (always works) plus, if present, the QEMU-WASM live emulator. Run 'make kernel'
# (or 'make web-qemu' for the emulator) first; CI does both.
web:
	@test -f $(KERNEL) || { echo "$(KERNEL) missing - run 'make kernel' first"; exit 1; }
	rm -rf $(WEB_OUT)
	mkdir -p $(WEB_OUT)/cast $(WEB_OUT)/vendor
	cp web/static/index.html web/static/styles.css web/static/app.js $(WEB_OUT)/
	cp web/vendor/coi-serviceworker.min.js $(WEB_OUT)/
	cp web/vendor/xterm.js web/vendor/xterm.css web/vendor/xterm-pty.js $(WEB_OUT)/vendor/
	@if ls web/vendor/qemu/* >/dev/null 2>&1 && [ -f web/vendor/qemu/qemu-system-riscv64.wasm ]; then \
		mkdir -p $(WEB_OUT)/vendor/qemu; \
		find web/vendor/qemu -type f ! -name .gitkeep -exec cp {} $(WEB_OUT)/vendor/qemu/ \; ; fi
	cp web/cast/honk-boot.log $(WEB_OUT)/cast/
	cp $(KERNEL) $(WEB_OUT)/honk.elf
	touch $(WEB_OUT)/.nojekyll
	@if [ -f $(WEB_OUT)/vendor/qemu/qemu-system-riscv64.wasm ]; then \
		echo "assembled $(WEB_OUT)/ (live emulator: present)"; \
	else echo "assembled $(WEB_OUT)/ (live emulator: replay-only - run 'make web-qemu')"; fi

# Build the QEMU-WASM bundle (Docker + emscripten; slow, infrequent).
web-qemu:
	bash web/build-qemu-wasm.sh

# Preview locally with the COOP/COEP headers the live emulator needs.
web-serve: web
	node web/test/serve.mjs $(WEB_OUT) 8088

clean:
	rm -f $(KERNEL)
	rm -rf $(WEB_OUT)

distclean: clean
	rm -rf .toolchain
