#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Generate the realistic sample captures used to verify ansipng.py.

Both samples are hand-written ANSI/UTF-8 byte streams that look like output
from a real TUI charting program:

  sample1.bin  a rendered braille line chart with a bordered panel, a reverse
               video title bar, 256-colour + truecolor, an OSC title sequence
               and a *truncated* escape sequence at the very end.

  sample2.bin  a repaint-heavy capture: CR/BS/TAB handling, scroll region,
               IL/DL/ICH/DCH/SU/SD, alternate screen, wide + combining
               characters, mouse/bracketed-paste modes, unknown and malformed
               escape sequences and invalid UTF-8 bytes.

Run:  python3 make_samples.py        (writes sample1.bin / sample2.bin here)
"""

import math
import os

E = "\x1b"
HERE = os.path.dirname(os.path.abspath(__file__))
INVALID = "@@INVALID-UTF8@@"          # replaced by raw 0xFF 0xFE bytes

BRAILLE_BITS = {
    (0, 0): 0x01, (0, 1): 0x02, (0, 2): 0x04, (1, 0): 0x08,
    (1, 1): 0x10, (1, 2): 0x20, (0, 3): 0x40, (1, 3): 0x80,
}


def braille_cell(dots):
    value = 0
    for dot in dots:
        value |= BRAILLE_BITS[dot]
    return chr(0x2800 + value)


def braille_plot(width, height, func):
    """Plot func(t)->0..1 as `height` strings of `width` braille cells."""
    dot_cols = width * 2
    dot_rows = height * 4
    heights = []
    for x in range(dot_cols):
        v = max(0.0, min(1.0, func(x / float(dot_cols - 1))))
        heights.append((1.0 - v) * (dot_rows - 1))
    cells = {}
    for x in range(dot_cols):
        y0 = int(round(heights[x]))
        y1 = int(round(heights[x + 1])) if x + 1 < dot_cols else y0
        for y in range(min(y0, y1), max(y0, y1) + 1):
            cells.setdefault((x // 2, y // 4), set()).add((x % 2, y % 4))
    out = []
    for cy in range(height):
        out.append("".join(braille_cell(cells.get((cx, cy), ()))
                           for cx in range(width)))
    return out


def ramp_color(i, n):
    """xterm-256 index along a green -> yellow -> orange -> red ramp."""
    stops = [28, 34, 40, 46, 82, 118, 154, 190, 226, 220, 214, 208, 202]
    return stops[min(len(stops) - 1, int(i * len(stops) / max(1, n)))]


def build_sample1(cols=100, rows=24):
    """A chart screen: frame, y/x axes, braille plot, histogram, legend."""
    out = []

    def w(s):
        out.append(s)

    def at(r, c=1):
        w("%s[%d;%dH" % (E, r, c))

    left, right = 3, 96
    top, bottom = 3, 22
    chart_left, chart_w, chart_h = 8, 68, 13

    w("%s[?1049h%s[2J%s[H%s[?25l" % (E, E, E, E))       # alt screen + clear
    w("%s]0;tui-chart - braille demo%s\\" % (E, E))      # OSC title: ignored

    # title bar (reverse video) -------------------------------------------
    title = " tui-chart  ::  requests/sec (braille, 2x4 dots per cell) "
    title += " " * max(0, cols - len(title))
    at(1)
    w("%s[7m%s%s[0m" % (E, title[:cols], E))

    # outer frame ----------------------------------------------------------
    at(top, left)
    w("\u250c" + "\u2500" * (right - left - 1) + "\u2510")
    for r in range(top + 1, bottom):
        at(r, left)
        w("\u2502")
        at(r, right)
        w("\u2502")
    at(bottom, left)
    w("\u2514" + "\u2500" * (right - left - 1) + "\u2518")

    # y axis ---------------------------------------------------------------
    for i, label in enumerate(("120", " 90", " 60", " 30", "  0")):
        row = top + 2 + i * 3
        at(row, left + 1)
        w("%s[38;5;245m%s%s[0m" % (E, label, E))
        at(row, left + 4)
        w("%s[38;5;238m\u2524%s[0m" % (E, E))

    # braille plot ---------------------------------------------------------
    func = (lambda t: 0.5 + 0.38 * math.sin(t * 9.0) * math.cos(t * 2.5)
            + 0.08 * math.sin(t * 41.0))
    for i, line in enumerate(braille_plot(chart_w, chart_h, func)):
        at(top + 1 + i, chart_left)
        w("%s[38;5;%dm%s%s[0m" % (E, ramp_color(i, chart_h), line, E))

    # x axis + labels ------------------------------------------------------
    at(top + chart_h + 2, chart_left)
    w("%s[38;5;238m" % E + "\u2534" * chart_w + ("%s[0m" % E))
    at(top + chart_h + 3, chart_left)
    w("%s[38;5;245m00:00     04:00     08:00     12:00     16:00     20:00%s[0m"
      % (E, E))

    # eighth-block histogram with truecolor --------------------------------
    hist_row = bottom - 2
    at(hist_row, left + 1)
    w("%s[38;5;245mhist%s[0m" % (E, E))
    for i in range(30):
        h = 1 + int(7 * abs(math.sin(i * 0.7)))
        green = 120 + i * 4
        at(hist_row + 1, chart_left + i)
        w("%s[38;2;%d;%d;120m%s%s[0m"
          % (E, 40 + i * 6, min(255, green), chr(0x2580 + h), E))

    # legend (outside the frame) -------------------------------------------
    at(bottom + 1)
    w("%s[48;5;28m  %s[0m ok   %s[48;5;214m  %s[0m warn   %s[48;5;196m  %s[0m crit"
      % (E, E, E, E, E, E))
    w("   %s[1;4mundocumented%s[0m %s[9mstrikethrough%s[0m" % (E, E, E, E))

    # deliberately truncated escape sequence at end of capture -------------
    w("%s[38;5;2" % E)
    return "".join(out).encode("utf-8")


def build_sample2(cols=100, rows=24):
    """A repaint-heavy / hostile capture: exercises robustness."""
    out = []

    def w(s):
        out.append(s)

    def at(r, c=1):
        w("%s[%d;%dH" % (E, r, c))

    w("%s[2J%s[H" % (E, E))
    w("%s[?2004h%s[?1000h%s[?1006h%s[?9999h" % (E, E, E, E))   # ignored modes
    w("%s[?25l" % E)

    # title bar written with CR/LF + EL, reverse video ---------------------
    w("%s[7m status: streaming               %s[K%s[0m\r\n" % (E, E, E))

    # repaint with \r and ESC[K -------------------------------------------
    w("connecting")
    w("\r%s[K%s[32mconnected%s[0m" % (E, E, E))
    w("\r\n")
    w("tab\there\tand\tcolumns\r\n")

    # scroll region + scroll + insert/delete ------------------------------
    w("%s[5;10r" % E)                     # DECSTBM rows 5..10
    at(5)
    for i in range(6):
        w("region line %d\r\n" % (i + 1))
    at(5)
    w("%s[L" % E)                          # insert line
    w("inserted line\r\n")
    at(6)
    w("%s[M" % E)                          # delete line
    w("deleted line\r\n")
    at(7, 3)
    w("%s[3@" % E)                         # insert chars
    w("<<")
    at(8, 3)
    w("%s[4P" % E)                         # delete chars
    w(">>")
    at(9)
    w("%s[S" % E)                          # scroll up (inside the region)
    w("after SU")
    w("%s[r" % E)                          # reset scroll region

    # wide + combining characters -----------------------------------------
    at(13)
    w("wide: \u4e2d\u6587\u30c6\u30b9\u30c8 mixing e\u0301 and a\u030a marks")
    at(14)
    w("box mix: \u250c\u2500\u252c\u2500\u2510  \u2554\u2550\u2566\u2550\u2557  "
      "\u256d\u2500\u253c\u2500\u256e  \u2588\u2593\u2592\u2591 \u28ff\u28c0")

    # repaint a panel over stale content ----------------------------------
    at(16)
    w("stale content that will be overwritten")
    at(16)
    w("%s[2K" % E)                         # erase whole line first
    w("\u250c\u2500 panel repainted " + "\u2500" * 12 + "\u2510")
    at(17)
    w("\u2502 %s[1;38;2;120;200;255mtruecolor inside a frame%s[0m \u2502"
      % (E, E))
    at(18)
    w("\u2514" + "\u2500" * 30 + "\u2518")

    # autowrap off at the right margin, then on again ----------------------
    at(20)
    w("%s[?7l" % E)
    w("no-wrap:" + "X" * 20)
    at(20, cols - 4)
    w("ZYXWVUTSRQ")                        # clipped, must not wrap
    w("%s[?7h" % E)

    # malformed / unknown sequences ---------------------------------------
    w("%sZ" % E)                           # unknown escape: skip one byte
    w("%s#8" % E)                          # DECALN-ish: ignored
    w("%s(B%s)0" % (E, E))                 # charset designators: no-op
    w("%s[>4;2m" % E)                      # modifyOtherKeys: ignored
    w("%sPq#0;2;0;0;0%s\\" % (E, E))       # DCS payload: skipped
    w("%s]0;window title (ignored)%s\\" % (E, E))   # OSC, properly closed
    w("\r\n")
    w(INVALID)                             # invalid UTF-8 -> U+FFFD
    w(" tail\r\n")
    w("%s[1;31;40mred on black%s[0m %s[0;7minverse%s[0m\r\n" % (E, E, E, E))
    # last bytes are an unterminated OSC: everything up to EOF is skipped
    w("%s]0;unterminated osc at eof" % E)
    return "".join(out).encode("utf-8").replace(INVALID.encode("ascii"),
                                                b"\xff\xfe")


def main():
    for name, data in (("sample1.bin", build_sample1()),
                       ("sample2.bin", build_sample2())):
        path = os.path.join(HERE, name)
        with open(path, "wb") as fh:
            fh.write(data)
        print("wrote %s (%d bytes)" % (path, len(data)))


if __name__ == "__main__":
    main()
