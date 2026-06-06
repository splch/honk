#!/usr/bin/env bash
# Run honk under QEMU virt: OpenSBI in M-mode, honk as the HS-mode payload.
#
# OpenSBI's fw_dynamic enters the boot trampoline (boot.bin) at 0x80200000,
# which jumps to honk.elf (loaded by -device loader at its link address).
#
#   MEM   QEMU RAM size   (default 512M; any size works - sized from the DTB)
#   SMP   number of harts (default 4)
#   HTTP  host port forwarded to honk's :80 httpd (default 8080)
#   SHARE host directory shared into honk over 9p (default ./share)
#
# honk drives a virtio-gpu framebuffer (M9). Under -nographic it runs headless
# (honk still draws the test pattern; see it with QMP `screendump`). To watch it
# live, drop -nographic and add a UI, e.g.:
#   SMP=4 qemu-system-riscv64 ... -serial mon:stdio -display cocoa  (or gtk/sdl)
#
# Quit an interactive session with Ctrl-A then x.
# With the defaults, `curl http://localhost:8080/` reaches honk's HTTP server,
# and the files under ./share appear inside honk (try `mount`, `ls`, `cat`).
set -euo pipefail
cd "$(dirname "$0")/.."

{ [ -f boot.bin ] && [ -f honk.elf ]; } || tools/build.sh

# Backing stores (created once, 16 MiB each). honk uses NVMe as the primary
# block device and virtio-blk as the fallback; both are attached so either path
# can be exercised.
NVME="${NVME:-nvme.img}"
DISK="${DISK:-disk.img}"
[ -f "$NVME" ] || dd if=/dev/zero of="$NVME" bs=1048576 count=16 2>/dev/null
[ -f "$DISK" ] || dd if=/dev/zero of="$DISK" bs=1048576 count=16 2>/dev/null

# Host file share (9p): honk mounts this directory read-only at boot.
SHARE="${SHARE:-share}"
mkdir -p "$SHARE"
[ -e "$SHARE/README.host" ] ||
	echo "This file lives on the host, shared into honk over 9p (mount tag 'host')." >"$SHARE/README.host"

exec qemu-system-riscv64 \
	-machine virt \
	-global virtio-mmio.force-legacy=false \
	-cpu rv64,h=true \
	-smp "${SMP:-4}" \
	-m "${MEM:-512M}" \
	-nographic \
	-bios default \
	-no-reboot \
	-kernel boot.bin \
	-device loader,file=honk.elf \
	-drive file="$NVME",if=none,id=nvm,format=raw \
	-device nvme,serial=honk,drive=nvm \
	-drive file="$DISK",if=none,id=blk0,format=raw \
	-device virtio-blk-device,drive=blk0 \
	-netdev "user,id=n0,hostfwd=tcp::${HTTP:-8080}-:80" \
	-device virtio-net-device,netdev=n0 \
	-device virtio-gpu-device \
	-device virtio-keyboard-device \
	-device virtio-tablet-device \
	-fsdev local,id=hostdev,path="$SHARE",security_model=none \
	-device virtio-9p-device,fsdev=hostdev,mount_tag=host
