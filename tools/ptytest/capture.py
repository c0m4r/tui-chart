#!/usr/bin/env python3
"""Drive a terminal UI under a real pty and capture everything it writes.

This is the end to end test harness for tui-chart: it runs the actual binary on
a pseudo terminal of a given size, sends keystrokes (and optional resize
events) on a schedule, and stores the raw byte stream. The capture can then be
replayed into a PNG with tools/ansipng.

usage:
  capture.py --cols 120 --rows 34 --out run.bin -- ./tui-chart --demo
  capture.py --cols 100 --rows 28 --keys "2.0:right,1.0:right,1.0:d" -- ./tui-chart
  capture.py --resize "3.0:90x24" --keys "1.0:?,1.0:esc,1.0:q" -- ./tui-chart

keys are given as DELAY:KEY pairs, delays are seconds to wait before sending.
supported key names: right left up down tab space enter esc backspace ctrl+c
plus any single character. --resize takes DELAY:COLSxROWS triples.
"""

from __future__ import annotations

import argparse
import errno
import fcntl
import os
import pty
import select
import signal
import struct
import sys
import termios
import time

KEYMAP = {
    "right": b"\x1b[C",
    "left": b"\x1b[D",
    "up": b"\x1b[A",
    "down": b"\x1b[B",
    "tab": b"\t",
    "space": b" ",
    "enter": b"\r",
    "esc": b"\x1b",
    "backspace": b"\x7f",
    "ctrl+c": b"\x03",
}


# Responses a real terminal sends back for the queries applications use to
# detect capabilities. Without these an app that probes the terminal can block.
RESPONSES = [
    (b"\x1b]11;?", b"\x1b]11;rgb:0000/0000/0000\x1b\\"),   # background colour
    (b"\x1b]10;?", b"\x1b]10;rgb:ffff/ffff/ffff\x1b\\"),   # foreground colour
    (b"\x1b]4;0;?", b"\x1b]4;0;rgb:0000/0000/0000\x1b\\"),
    (b"\x1b[6n", b"\x1b[1;1R"),                                # cursor position
    (b"\x1b[c", b"\x1b[?62;22c"),                              # primary device attrs
    (b"\x1b[>c", b"\x1b[>0;136;0c"),                           # secondary device attrs
]


def answer_queries(fd: int, data: bytes, seen: bytearray, replied: set) -> None:
    """Reply to terminal capability queries exactly once per query type.

    Replying more than once is worse than not replying at all: the extra bytes
    reach the application as keyboard input and corrupt its key handling.
    """
    seen.extend(data)
    for query, reply in RESPONSES:
        if query in replied:
            continue
        if seen.find(query) < 0:
            continue
        try:
            os.write(fd, reply)
        except OSError:
            return
        replied.add(query)


def set_winsize(fd: int, cols: int, rows: int) -> None:
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))


def parse_schedule(spec: str):
    """Parse "DELAY:VALUE,DELAY:VALUE" into absolute times.

    Delays accumulate, so "1.0:right,0.5:right" fires at t=1.0 and t=1.5.
    """
    out = []
    if not spec:
        return out
    at = 0.0
    for item in spec.split(","):
        item = item.strip()
        if not item:
            continue
        delay, _, value = item.partition(":")
        at += float(delay)
        out.append((at, value))
    return out


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--cols", type=int, default=120)
    ap.add_argument("--rows", type=int, default=34)
    ap.add_argument("--out", default="capture.bin")
    ap.add_argument("--keys", default="")
    ap.add_argument("--resize", default="")
    ap.add_argument("--settle", type=float, default=1.0,
                    help="seconds to wait after the last action before quitting")
    ap.add_argument("--timeout", type=float, default=60.0)
    ap.add_argument("cmd", nargs=argparse.REMAINDER)
    args = ap.parse_args()

    cmd = args.cmd
    if cmd and cmd[0] == "--":
        cmd = cmd[1:]
    if not cmd:
        ap.error("no command given")

    keys = parse_schedule(args.keys)
    resizes = parse_schedule(args.resize)

    pid, fd = pty.fork()
    if pid == 0:  # child
        os.environ["TERM"] = os.environ.get("PTYTEST_TERM", "xterm-256color")
        os.environ.setdefault("COLORTERM", "truecolor")
        os.environ["LINES"] = str(args.rows)
        os.environ["COLUMNS"] = str(args.cols)
        try:
            os.execvp(cmd[0], cmd)
        except OSError as exc:
            os.write(2, f"exec failed: {exc}\n".encode())
            os._exit(127)

    set_winsize(fd, args.cols, args.rows)

    captured = bytearray()
    answered = bytearray()
    replied = set()
    start = time.time()
    deadline = start + args.timeout
    pending = [(start + d, "key", v) for d, v in keys] + \
              [(start + d, "resize", v) for d, v in resizes]
    pending.sort()
    finished_actions_at = None

    try:
        while True:
            now = time.time()
            if now > deadline:
                break
            while pending and pending[0][0] <= now:
                _, kind, value = pending.pop(0)
                if kind == "key":
                    seq = KEYMAP.get(value, value.encode())
                    os.write(fd, seq)
                else:
                    cols, _, rows = value.partition("x")
                    set_winsize(fd, int(cols), int(rows))
                    os.kill(pid, signal.SIGWINCH)
                finished_actions_at = now
            timeout = 0.05
            if pending:
                timeout = min(timeout, max(0.0, pending[0][0] - now))
            try:
                r, _, _ = select.select([fd], [], [], timeout)
            except InterruptedError:
                continue
            if r:
                try:
                    data = os.read(fd, 65536)
                except OSError as exc:
                    if exc.errno == errno.EIO:  # child closed the pty
                        break
                    raise
                if not data:
                    break
                captured.extend(data)
                answer_queries(fd, data, answered, replied)
            elif finished_actions_at is not None and not pending:
                if now - finished_actions_at >= args.settle:
                    break
            elif not pending and now - start >= 2.0 and not captured:
                break
    finally:
        with open(args.out, "wb") as fh:
            fh.write(bytes(captured))
        try:
            os.kill(pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        try:
            os.waitpid(pid, 0)
        except ChildProcessError:
            pass

    print(f"captured {len(captured)} bytes -> {args.out}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
