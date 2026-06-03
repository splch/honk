# honk — a small RISC-V 64-bit OS in pure Go (TamaGo unikernel).
# See DESIGN.md (why) and RV64.md (hardware).  Quickstart: make toolchain && make qemu
#
# Two boards (TARGET=):
#   sifive_u  Phase 0 — machine-mode boot via a trampoline BIOS (TamaGo's port)
#   virt      Phase 1 — S-mode payload under OpenSBI (honk's own board, default)
#
# Milestone-0 gotchas, baked in below so they never bite again:
#   * mise exports GOROOT (the stock Go); it overrides tamago-go's own GOROOT and
#     makes `compile` reject GOOS=tamago ("unknown goos tamago"). Every toolchain
#     call therefore runs under `env -u GOROOT`.
#   * GOTOOLCHAIN=local stops the go.mod `go` line from re-exec'ing a stock Go.
#   * sifive_u only: `go mod download` before the DTB (the .dts lives in the
#     module cache); the trampoline BIOS is assembled with riscv64-elf-gcc.
#   * macOS has no `timeout`, so `smoke` backgrounds QEMU then kills it.

SHELL := /bin/bash

APP        := honk
TARGET     ?= virt
GOOSPKG    := github.com/usbarmory/tamago
RISCV_GCC  ?= riscv64-elf-gcc
RISCV_OBJCOPY ?= riscv64-elf-objcopy
BUILD      := build

# tamago-go toolchain: override TAMAGO=/path/to/go, or run `make toolchain`.
TAMAGO_DIR    ?= $(HOME)/.cache/tamago-go
TAMAGO        ?= $(TAMAGO_DIR)/bin/go
TAMAGO_BRANCH ?= latest

# Every tamago toolchain invocation, with the GOROOT/GOTOOLCHAIN fixes applied.
GO    := env -u GOROOT GOTOOLCHAIN=local $(TAMAGO)
GOENV := GOOS=tamago GOARCH=riscv64 GOOSPKG=$(GOOSPKG)

ifeq ($(TARGET),virt)
  TEXT_START  := 0x80210000        # RamStart (0x80200000) + 0x10000, above OpenSBI
  LOADBASE    := 0x8020f000        # TEXT_START - 0x1000: ELF-header page OpenSBI enters
  # linkcpuinit: use honk's S-mode cpuinit (boot_riscv64.s) instead of the
  # machine-mode one in tamago/riscv64 (imported for riscv64.CPU; see time.go).
  TAGS        := virt,linkcpuinit
  # honk has no RTC; inject the build time so the wall clock is plausible for TLS
  # (DESIGN.md §15.5). NTP refines it at runtime.
  LDX         := -X 'github.com/splch/honk/internal/board/virt.buildUnixStr=$(shell date +%s)'
  KERNEL_DEPS := $(BUILD)/$(APP) $(BUILD)/trampoline.bin $(BUILD)/disk.img
  QEMU := qemu-system-riscv64 -machine virt -m 512M -smp 1 -bios default \
          -nographic -monitor none -serial stdio -no-reboot \
          -global virtio-mmio.force-legacy=false
  # loaded AFTER -kernel (see recipes) so the trampoline wins the load-base address
  QEMU_EXTRA := -device loader,file=$(BUILD)/trampoline.bin,addr=$(LOADBASE) \
                -drive file=$(BUILD)/disk.img,format=raw,if=none,id=hd0 \
                -device virtio-blk-device,drive=hd0 \
                -netdev user,id=net0,hostfwd=tcp:127.0.0.1:8080-:80,hostfwd=tcp:127.0.0.1:2222-:22 -device virtio-net-device,netdev=net0 \
                -object rng-builtin,id=rng0 -device virtio-rng-device,rng=rng0
else ifeq ($(TARGET),sifive_u)
  TEXT_START  := 0x80010000        # fu540 ramStart (0x80000000) + 0x10000
  TAGS        := sifive_u,semihosting
  LDX         :=
  KERNEL_DEPS := $(BUILD)/$(APP) $(BUILD)/qemu.dtb $(BUILD)/bios.bin
  QEMU_EXTRA :=
  QEMU := qemu-system-riscv64 -machine sifive_u -m 512M -nographic -monitor none \
          -semihosting -serial stdio -net none -dtb $(BUILD)/qemu.dtb -bios $(BUILD)/bios.bin
else
  $(error unknown TARGET '$(TARGET)' — use 'virt' or 'sifive_u')
endif

GOFLAGS := -tags $(TAGS) -trimpath -ldflags "-T $(TEXT_START) -R 0x1000 $(LDX)"
SRC     := $(shell find . \( -name '*.go' -o -name '*.s' \) -not -path './$(BUILD)/*' 2>/dev/null)

.PHONY: all elf qemu qemu-gdb smoke test vet fmt toolchain check-tamago clean help

all: elf
elf: $(BUILD)/$(APP)

help:
	@echo "honk targets (TARGET=virt|sifive_u, default $(TARGET)):"
	@echo "  make toolchain   build the tamago-go compiler into $(TAMAGO_DIR) (~40s, one time)"
	@echo "  make elf         build honk (RV64 ELF, GOOS=tamago)"
	@echo "  make qemu        boot honk in QEMU                  (quit: Ctrl-A x)"
	@echo "  make qemu-gdb    boot frozen, gdb on tcp::1234"
	@echo "  make smoke       non-interactive boot + banner check (CI)"
	@echo "  make test        host-side unit tests: go test -race ./internal/..."
	@echo "  make vet / fmt   go vet (under tamago) / gofmt -w"
	@echo "  make clean       remove ./$(BUILD)"
	@echo "  TAMAGO=$(TAMAGO)"

check-tamago:
	@test -x "$(TAMAGO)" || { \
	  echo "tamago-go not found at: $(TAMAGO)"; \
	  echo "run 'make toolchain', or set TAMAGO=/path/to/tamago-go/bin/go"; \
	  exit 1; }

toolchain:
	@if [ -x "$(TAMAGO)" ]; then echo "tamago-go already present: $(TAMAGO)"; exit 0; fi
	mkdir -p "$(dir $(TAMAGO_DIR))"
	git clone --depth 1 --branch $(TAMAGO_BRANCH) https://github.com/usbarmory/tamago-go.git "$(TAMAGO_DIR)"
	cd "$(TAMAGO_DIR)/src" && env -u GOROOT GOROOT_BOOTSTRAP="$$(go env GOROOT)" ./make.bash
	@echo "built tamago-go: $(TAMAGO)"

$(BUILD):
	@mkdir -p $(BUILD)

$(BUILD)/$(APP): check-tamago $(SRC) go.mod | $(BUILD)
	$(GOENV) $(GO) build $(GOFLAGS) -o $@ .

# --- sifive_u-only artifacts (DTB + trampoline BIOS) ---
$(BUILD)/qemu.dtb: check-tamago | $(BUILD)
	$(GO) mod download $(GOOSPKG)
	dtc -I dts -O dtb \
	  "$$($(GO) env GOMODCACHE)/$$($(GO) list -m -f '{{.Path}}@{{.Version}}' $(GOOSPKG))/board/qemu/sifive_u/qemu-riscv64-sifive_u.dts" \
	  -o $@ 2>/dev/null

$(BUILD)/bios.bin: $(BUILD)/$(APP) boot/sifive_u/bios.s boot/sifive_u/bios.ld | $(BUILD)
	RT0=$$($(GO) tool nm $(BUILD)/$(APP) | awk '$$NF=="_rt0_riscv64_tamago"{print $$1}'); \
	  echo ".equ RT0_RISCV64_TAMAGO, 0x$$RT0" > $(BUILD)/bios.cfg
	$(RISCV_GCC) -I$(BUILD) -march=rv64g -mabi=lp64 -static -mcmodel=medany \
	  -fvisibility=hidden -nostdlib -nostartfiles -Tboot/sifive_u/bios.ld \
	  boot/sifive_u/bios.s -o $@

# --- virt-only: trampoline jumped to by OpenSBI at the kernel load base ---
$(BUILD)/trampoline.bin: $(BUILD)/$(APP) boot/virt/trampoline.s | $(BUILD)
	RT0=$$($(GO) tool nm $(BUILD)/$(APP) | awk '$$NF=="_rt0_riscv64_tamago"{print $$1}'); \
	  echo ".equ RT0_RISCV64_TAMAGO, 0x$$RT0" > $(BUILD)/trampoline.cfg
	$(RISCV_GCC) -I$(BUILD) -march=rv64g -mabi=lp64 -nostdlib -nostartfiles \
	  -Ttext=$(LOADBASE) boot/virt/trampoline.s -o $(BUILD)/trampoline.elf
	$(RISCV_OBJCOPY) -O binary $(BUILD)/trampoline.elf $@

# virt-only: a blank 64 MiB image for the virtio-blk disk. honk formats it as
# FAT32 on first boot (seeding a motd) and its contents then persist across
# reboots (DESIGN.md §15, step 7). Recreated only when missing, so writes survive
# repeated `make run`; `make clean` resets it.
$(BUILD)/disk.img: | $(BUILD)
	dd if=/dev/zero of=$@ bs=1m count=64 2>/dev/null

qemu: $(KERNEL_DEPS)
	$(QEMU) -kernel $(BUILD)/$(APP) $(QEMU_EXTRA)

qemu-gdb: $(KERNEL_DEPS)
	$(QEMU) -kernel $(BUILD)/$(APP) $(QEMU_EXTRA) -S -s

smoke: $(KERNEL_DEPS)
	@echo "smoke: booting honk (TARGET=$(TARGET), 12s capture)..."
	@$(QEMU) -kernel $(BUILD)/$(APP) $(QEMU_EXTRA) < /dev/null > $(BUILD)/smoke.log 2>&1 & \
	  QPID=$$!; sleep 12; kill -TERM $$QPID 2>/dev/null; sleep 1; \
	  kill -KILL $$QPID 2>/dev/null; wait $$QPID 2>/dev/null; true
	@echo "--- smoke.log ---"; sed -E 's/\x1b\[[0-9;]*m//g' $(BUILD)/smoke.log
	@grep -q honk $(BUILD)/smoke.log && echo "smoke: PASS" || { echo "smoke: FAIL"; exit 1; }

test:
	go test -race ./internal/...

vet: check-tamago
	$(GOENV) $(GO) vet -tags $(TAGS) ./...

fmt:
	gofmt -l -w .

clean:
	rm -rf $(BUILD)
