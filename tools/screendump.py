#!/usr/bin/env python3
# Capture honk's virtio-gpu scanout from a running QEMU over QMP, with no
# display attached, and optionally verify the M9 test pattern.
#
# QEMU keeps the graphical console surface in memory even under -display none,
# so `screendump` works headlessly - this is how the smoke test proves honk's
# virtio-gpu driver actually pushed pixels to the host framebuffer (not just
# that the control commands succeeded).
#
#   usage: screendump.py [--check] <qmp.unix.sock> <out.ppm>
#
# Prints the resolution and (with --check) the four quadrant-center colors,
# then "GPU PATTERN OK" / "GPU PATTERN FAIL"; exits non-zero on a failed check.
# The expected pattern is kernel/display.go paintTestPattern: channel-distinct
# quadrants (red / green / blue / near-white), which also catch a swapped pixel
# format in the driver.

import json
import os
import socket
import sys
import time


def qmp_screendump(sock_path, out_ppm):
    for _ in range(100):
        if os.path.exists(sock_path):
            break
        time.sleep(0.1)
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    for _ in range(100):
        try:
            s.connect(sock_path)
            break
        except OSError:
            time.sleep(0.1)
    else:
        raise SystemExit("screendump: could not connect to QMP socket")
    f = s.makefile("rwb", buffering=0)

    def recv():
        line = f.readline()
        return json.loads(line) if line else None

    def cmd(obj):
        f.write((json.dumps(obj) + "\n").encode())
        while True:
            r = recv()
            if r is None:
                return None
            if "return" in r or "error" in r:
                return r

    recv()  # QMP greeting
    cmd({"execute": "qmp_capabilities"})
    r = cmd({"execute": "screendump", "arguments": {"filename": out_ppm}})
    cmd({"execute": "quit"})
    if not r or "error" in r:
        raise SystemExit(f"screendump: QMP error: {r}")


def read_ppm(path):
    data = open(path, "rb").read()
    if data[:2] != b"P6":
        raise SystemExit("screendump: not a P6 PPM")
    i, tok = 2, []
    while len(tok) < 3:
        while i < len(data) and data[i] in b" \t\n\r":
            i += 1
        if data[i : i + 1] == b"#":
            while i < len(data) and data[i] not in b"\n":
                i += 1
            continue
        s = i
        while i < len(data) and data[i] not in b" \t\n\r":
            i += 1
        tok.append(int(data[s:i]))
    i += 1  # single whitespace separator after maxval
    w, h, _ = tok
    return w, h, data[i:]


def check_pattern(w, h, px):
    def at(x, y):
        o = (y * w + x) * 3
        return (px[o], px[o + 1], px[o + 2])

    quads = {
        "TL": at(w // 4, h // 4),
        "TR": at(3 * w // 4, h // 4),
        "BL": at(w // 4, 3 * h // 4),
        "BR": at(3 * w // 4, 3 * h // 4),
    }
    for name, rgb in quads.items():
        print(f"quad {name} rgb {rgb}")

    def dominant(rgb, ch):
        return rgb[ch] >= 120 and all(rgb[ch] >= rgb[o] + 60 for o in range(3) if o != ch)

    ok = (
        dominant(quads["TL"], 0)  # red
        and dominant(quads["TR"], 1)  # green
        and dominant(quads["BL"], 2)  # blue
        and all(c >= 180 for c in quads["BR"])  # white
    )
    return ok


def main():
    args = sys.argv[1:]
    check = False
    if args and args[0] == "--check":
        check = True
        args = args[1:]
    if len(args) != 2:
        raise SystemExit("usage: screendump.py [--check] <qmp.sock> <out.ppm>")
    sock_path, out_ppm = args

    qmp_screendump(sock_path, out_ppm)
    w, h, px = read_ppm(out_ppm)
    print(f"screen {w}x{h}")
    if not check:
        return
    ok = check_pattern(w, h, px)
    print("GPU PATTERN OK" if ok else "GPU PATTERN FAIL")
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
