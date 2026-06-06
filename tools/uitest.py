#!/usr/bin/env python3
# Drive honk's M10 GUI demo headlessly over QMP: click the button, focus the
# text field, type a string, then screendump and confirm the click registered
# (the button's toggled-on fill is a distinct green). honk also logs each event
# to its serial console, which the smoke test asserts as the primary proof; this
# script is the visual, end-to-end confirmation that input reached the toolkit
# and the toolkit repainted the framebuffer.
#
#   usage: uitest.py <qmp.sock> <out.ppm> <btnX> <btnY> <fieldX> <fieldY> <type>
#
# Coordinates are screen pixels (the demo's button/field centers); this script
# learns the resolution via an initial screendump and scales them to the
# tablet's absolute axis range.

import json
import os
import socket
import sys
import time

ABS_MAX = 32768  # honk scales tablet axes by this (kernel/ui.go absRange)


def connect(sock_path):
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
        raise SystemExit("uitest: could not connect to QMP socket")
    return s.makefile("rwb", buffering=0)


def make_qmp(f):
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

    recv()  # greeting
    cmd({"execute": "qmp_capabilities"})
    return cmd


def send(cmd, events):
    cmd({"execute": "input-send-event", "arguments": {"events": events}})


def screendump(cmd, path):
    cmd({"execute": "screendump", "arguments": {"filename": path}})


def read_ppm(path):
    data = open(path, "rb").read()
    if data[:2] != b"P6":
        raise SystemExit("uitest: not a P6 PPM")
    i, tok = 2, []
    while len(tok) < 3:
        while i < len(data) and data[i] in b" \t\n\r":
            i += 1
        s = i
        while i < len(data) and data[i] not in b" \t\n\r":
            i += 1
        tok.append(int(data[s:i]))
    i += 1
    w, h, _ = tok
    return w, h, data[i:]


def qcode(ch):
    if ch == " ":
        return "spc"
    return ch  # letters and digits map to their own qcode


def main():
    a = sys.argv[1:]
    if len(a) != 7:
        raise SystemExit("usage: uitest.py <sock> <ppm> <btnX> <btnY> <fieldX> <fieldY> <type>")
    sock, ppm = a[0], a[1]
    bx, by, fx, fy = (int(a[2]), int(a[3]), int(a[4]), int(a[5]))
    typ = a[6]

    cmd = make_qmp(connect(sock))

    # Learn the resolution so pixel targets scale to the tablet axis range.
    screendump(cmd, ppm)
    w, h, _ = read_ppm(ppm)
    ax = lambda x: max(0, min(ABS_MAX - 1, x * ABS_MAX // w))
    ay = lambda y: max(0, min(ABS_MAX - 1, y * ABS_MAX // h))

    def tap(x, y):
        send(cmd, [
            {"type": "abs", "data": {"axis": "x", "value": ax(x)}},
            {"type": "abs", "data": {"axis": "y", "value": ay(y)}},
            {"type": "btn", "data": {"down": True, "button": "left"}},
        ])
        time.sleep(0.05)
        send(cmd, [{"type": "btn", "data": {"down": False, "button": "left"}}])
        time.sleep(0.1)

    tap(bx, by)  # click the button (toggles it on)
    tap(fx, fy)  # click the field (gives it keyboard focus)
    for ch in typ:
        send(cmd, [{"type": "key", "data": {"down": True, "key": {"type": "qcode", "data": qcode(ch)}}}])
        send(cmd, [{"type": "key", "data": {"down": False, "key": {"type": "qcode", "data": qcode(ch)}}}])
        time.sleep(0.03)
    time.sleep(0.3)  # let honk's input pump process and repaint

    screendump(cmd, ppm)
    cmd({"execute": "quit"})

    w, h, px = read_ppm(ppm)
    o = (by * w + bx) * 3
    r, g, b = px[o], px[o + 1], px[o + 2]
    print(f"screen {w}x{h}")
    print(f"button rgb ({r}, {g}, {b})")
    ok = g >= 150 and g > r + 40 and g > b + 40  # the toggled-on green fill
    print("UI CLICK OK" if ok else "UI CLICK FAIL")
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
