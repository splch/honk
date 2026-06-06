# honk - build and run the pure-Go RISC-V 64 OS.

CORE_VERSION ?= 1

.PHONY: all kernel run clean fmt vet test smoke phase-a coreimg

all: kernel

# Build the kernel ELF + boot trampoline (auto-installs tamago-go on first use;
# build.sh also rebuilds the embedded core image).
kernel honk.elf boot.bin:
	tools/build.sh

# Build the signed, Merkle-tree'd immutable core image the kernel embeds.
coreimg kernel/core.img:
	env -u GOOS -u GOARCH -u GOOSPKG go run ./tools/mkimage -version $(CORE_VERSION) kernel/core kernel/core.img

# Boot honk under QEMU virt (OpenSBI M-mode firmware, honk as HS-mode payload).
run: honk.elf boot.bin
	tools/run-qemu.sh

fmt:
	gofmt -w kernel board block tools

# vet runs under the tamago toolchain so the GOOS=tamago files are analyzed.
# The embedded core image must exist first (kernel/main embeds it).
vet: coreimg
	tools/vet.sh

# Host race tests for the portable, pure-Go packages (process model, console
# input ring, storage, image verity).
test:
	go test -race -count=1 ./kernel/proc/ ./board/virt/ring/ ./kernel/kv/ ./kernel/vfs/ ./kernel/image/ ./block/

# Build + boot under QEMU and assert expected output (CI gate).
smoke:
	tools/smoke-test.sh

# Phase A (M0/M1/M2) acceptance: host race tests + a focused QEMU boot matrix
# (SMP 1/4/8, RAM 256M-2G, console + line editing, the fatal trap path, and the
# live process model). No storage attached - Phase A stands on the core alone.
phase-a:
	tools/phase-a-test.sh

clean:
	rm -f honk.elf boot.bin kernel/core.img
