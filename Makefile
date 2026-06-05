# honk - build and run the pure-Go RISC-V 64 OS.

GOENV := GOOS=tamago GOARCH=riscv64 GOOSPKG=github.com/usbarmory/tamago
TAMAGO := go run github.com/usbarmory/tamago/cmd/tamago

.PHONY: all kernel run clean fmt vet test smoke

all: kernel

# Build the kernel ELF + boot trampoline (auto-installs tamago-go on first use).
kernel honk.elf boot.bin:
	tools/build.sh

# Boot honk under QEMU virt (OpenSBI M-mode firmware, honk as HS-mode payload).
run: honk.elf boot.bin
	tools/run-qemu.sh

fmt:
	gofmt -w kernel board

# vet runs under the tamago toolchain so the GOOS=tamago files are analyzed.
vet:
	$(GOENV) $(TAMAGO) vet ./kernel ./kernel/proc ./board/...

# Host race tests for the portable, pure-Go packages (e.g. the process model).
test:
	go test -race -count=1 ./kernel/proc/...

# Build + boot under QEMU and assert expected output (CI gate).
smoke:
	tools/smoke-test.sh

clean:
	rm -f honk.elf boot.bin
