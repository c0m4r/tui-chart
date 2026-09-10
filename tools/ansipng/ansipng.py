#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""ansipng -- render captured terminal output (raw ANSI/VT bytes) into a PNG.

This is a *verification* tool: it replays a byte stream captured from a terminal
application (e.g. a TUI charting app) through a small VT100/xterm style terminal
emulator and rasterises the resulting screen, so a human or an agent can look at
what the program actually drew.

It is intentionally self contained: Python 3 standard library + Pillow only.

Usage
-----
    python3 ansipng.py INPUT [OUTPUT.png] [-w COLS] [-r ROWS] [--font PATH]
                      [--cell-w N] [--cell-h N] [--scale F]
                      [--bg RRGGBB] [--fg RRGGBB] [--dump-text] [--self-test]

Examples
--------
    python3 ansipng.py capture.bin                 # -> capture.png
    python3 ansipng.py capture.bin shot.png -w 100 -r 30 --scale 2
    python3 ansipng.py capture.bin --dump-text     # text dump + PNG
    python3 ansipng.py --self-test                 # synthetic smoke test

Design notes
------------
* The parser is byte oriented and forgiving: unknown escape sequences are
  skipped, truncated sequences at EOF are dropped, invalid UTF-8 is replaced.
* Glyphs that a preview really must get right (Braille patterns, block
  elements, box drawing) are drawn as *geometry* rather than font text.
* Wide (East Asian Wide/Fullwidth) characters occupy two cells, combining
  marks are attached to the preceding cell and drawn over it.

Known limitations
-----------------
* No scrollback: only the final visible screen is rendered (ED 3 is a no-op).
* No query responses: DSR/DA/window-size queries are ignored, so a capture
  that was waiting for a reply may look incomplete.
* DEC line-drawing charset translation is intentionally *not* applied, because
  captured output is expected to be UTF-8 already.
* DejaVuSansMono has no CJK/emoji glyphs, so such characters are drawn as
  "tofu" boxes (exactly like a terminal without a fallback font).
* Bold/italic use DejaVuSansMono-Bold/-Oblique when they sit next to the
  regular font file, otherwise bold is faked by double-striking.
* Double-line box drawing and mixed light/heavy variants are approximations
  drawn from 1-2px strokes.
"""

from __future__ import annotations

import argparse
import codecs
import math
import os
import sys
import unicodedata

from PIL import Image, ImageDraw, ImageFont

# --------------------------------------------------------------------------
# constants
# --------------------------------------------------------------------------

VERSION = "1.0"

DEFAULT_COLS = 120
DEFAULT_ROWS = 35
DEFAULT_CELL_W = 9
DEFAULT_CELL_H = 18
DEFAULT_SCALE = 1.0
DEFAULT_BG = "101216"
DEFAULT_FG = "c8ccd4"
DEFAULT_FONT = "/usr/share/fonts/TTF/DejaVuSansMono.ttf"
FALLBACK_FONTS = (
    "/usr/share/fonts/TTF/DejaVuSansMono.ttf",
    "/usr/share/fonts/dejavu/DejaVuSansMono.ttf",
    "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf",
    "/usr/share/fonts/dejavu-fonts-ttf/DejaVuSansMono.ttf",
)

# SGR attribute bits
A_BOLD = 1 << 0
A_DIM = 1 << 1
A_ITALIC = 1 << 2
A_UNDERLINE = 1 << 3
A_REVERSE = 1 << 4
A_STRIKE = 1 << 5

# parser states
S_GROUND, S_ESC, S_CSI, S_OSC, S_OSC_ESC, S_STR, S_CHARSET = range(7)

# xterm 256 colour palette: 16 base colours + 6x6x6 cube + 24 greys
_BASE16 = (
    (0, 0, 0), (205, 0, 0), (0, 205, 0), (205, 205, 0),
    (0, 0, 238), (205, 0, 205), (0, 205, 205), (229, 229, 229),
    (127, 127, 127), (255, 0, 0), (0, 255, 0), (255, 255, 0),
    (92, 92, 255), (255, 0, 255), (0, 255, 255), (255, 255, 255),
)
_CUBE_LEVELS = (0, 95, 135, 175, 215, 255)


def _build_palette():
    pal = list(_BASE16)
    for r in _CUBE_LEVELS:
        for g in _CUBE_LEVELS:
            for b in _CUBE_LEVELS:
                pal.append((r, g, b))
    for i in range(24):
        v = 8 + i * 10
        pal.append((v, v, v))
    return tuple(pal)


PALETTE256 = _build_palette()


def _r(v):
    """Round half up (python's round() is banker's rounding, which we don't want)."""
    return int(math.floor(v + 0.5))


def clamp(v, lo, hi):
    return lo if v < lo else (hi if v > hi else v)


def blend(fg, bg, alpha):
    """alpha * fg over bg."""
    return tuple(clamp(_r(f * alpha + b * (1.0 - alpha)), 0, 255)
                 for f, b in zip(fg, bg))


def dim_color(c, factor=0.6):
    return tuple(clamp(_r(v * factor), 0, 255) for v in c)


def parse_color(text, default=None):
    """Accept RRGGBB, #RRGGBB, RGB, #RGB, 'r,g,b' or 'none'."""
    if text is None:
        return default
    s = str(text).strip()
    if not s:
        return default
    if s.lower() in ("none", "default", "-"):
        return None
    if "," in s:
        try:
            parts = [int(p) for p in s.split(",")]
            if len(parts) == 3:
                return tuple(clamp(p, 0, 255) for p in parts)
        except ValueError:
            return default
        return default
    s = s.lstrip("#")
    try:
        if len(s) == 3:
            return tuple(int(ch * 2, 16) for ch in s)
        if len(s) == 6:
            return (int(s[0:2], 16), int(s[2:4], 16), int(s[4:6], 16))
        if len(s) == 8:  # RRGGBBAA -> ignore alpha
            return (int(s[0:2], 16), int(s[2:4], 16), int(s[4:6], 16))
    except ValueError:
        return default
    return default


def wcwidth(ch):
    """Minimal wcwidth: 0 for combining/format, 2 for East Asian W/F, else 1."""
    if not ch:
        return 1
    cat = unicodedata.category(ch)
    if cat in ("Mn", "Me", "Cf"):
        return 0
    if cat == "Cc":
        return 0
    if unicodedata.combining(ch):
        return 0
    if unicodedata.east_asian_width(ch) in ("W", "F"):
        return 2
    return 1


# --------------------------------------------------------------------------
# screen model
# --------------------------------------------------------------------------

class Cell(object):
    __slots__ = ("ch", "comb", "fg", "bg", "attr", "width", "cont")

    def __init__(self, ch=" ", fg=None, bg=None, attr=0):
        self.ch = ch          # character ("" for a wide-char continuation cell)
        self.comb = None      # list of combining marks drawn over this cell
        self.fg = fg          # RGB tuple or None = default fg
        self.bg = bg          # RGB tuple or None = default bg
        self.attr = attr      # A_* bit mask
        self.width = 1        # 1 or 2
        self.cont = False     # True for the 2nd cell of a wide character


class Buffer(object):
    """One screen buffer (main or alternate)."""

    def __init__(self, cols, rows):
        self.cols = cols
        self.rows = rows
        self.grid = [[Cell() for _ in range(cols)] for _ in range(rows)]
        self.x = 0
        self.y = 0
        self.top = 0
        self.bottom = rows - 1
        self.autowrap = True
        self.wrap_pending = False
        self.cursor_visible = True
        self.saved = None
        self.last_char = " "
        self.last_width = 1

    def resize(self, cols, rows):
        grid = [[Cell() for _ in range(cols)] for _ in range(rows)]
        for y in range(min(rows, self.rows)):
            row = self.grid[y]
            for x in range(min(cols, self.cols)):
                grid[y][x] = row[x]
        self.grid = grid
        self.cols = cols
        self.rows = rows
        self.x = clamp(self.x, 0, cols - 1)
        self.y = clamp(self.y, 0, rows - 1)
        self.top = 0
        self.bottom = rows - 1


class Pen(object):
    __slots__ = ("fg", "bg", "attr")

    def __init__(self):
        self.fg = None
        self.bg = None
        self.attr = 0

    def copy(self):
        p = Pen()
        p.fg, p.bg, p.attr = self.fg, self.bg, self.attr
        return p


class Terminal(object):
    """A small VT100/xterm-ish terminal emulator, enough for TUI capture replay."""

    def __init__(self, cols=DEFAULT_COLS, rows=DEFAULT_ROWS):
        self.cols = max(1, int(cols))
        self.rows = max(1, int(rows))
        self.pen = Pen()
        self.main = Buffer(self.cols, self.rows)
        self.alt = Buffer(self.cols, self.rows)
        self.buf = self.main
        self.using_alt = False
        self.decoder = codecs.getincrementaldecoder("utf-8")("replace")
        self._reset_parser()

    # -- parser plumbing ---------------------------------------------------

    def _reset_parser(self):
        self.state = S_GROUND
        self.text_buf = bytearray()
        self.csi_params = bytearray()
        self.csi_inter = bytearray()
        # number of UTF-8 continuation bytes still expected by the incremental
        # decoder; needed so that 0x9B/0x9C inside a multi-byte character are
        # not mistaken for C1 control codes.
        self.utf8_pending = 0

    def _text_byte(self, b):
        self.text_buf.append(b)
        if self.utf8_pending > 0:
            self.utf8_pending -= 1
        elif b >= 0xF0:
            self.utf8_pending = 3
        elif b >= 0xE0:
            self.utf8_pending = 2
        elif b >= 0xC2:
            self.utf8_pending = 1
        # stray continuation bytes are left to the decoder (-> U+FFFD)

    def _flush_text(self):
        if not self.text_buf:
            return
        data = bytes(self.text_buf)
        del self.text_buf[:]
        try:
            text = self.decoder.decode(data, False)
        except Exception:
            text = data.decode("utf-8", "replace")
            self.utf8_pending = 0
        if text:
            self._emit_text(text)

    def _emit_text(self, text):
        for ch in text:
            self.put_char(ch, wcwidth(ch))

    def feed(self, data):
        """Feed raw captured bytes."""
        i = 0
        n = len(data)
        while i < n:
            b = data[i]
            i += 1
            state = self.state
            if state == S_GROUND:
                if b == 0x1B:
                    self._flush_text()
                    self.state = S_ESC
                elif 0x20 <= b < 0x7F:
                    self._text_byte(b)
                elif b == 0x7F:
                    self._flush_text()     # DEL: ignore
                elif b < 0x20:
                    self._flush_text()
                    self._exec_control(b)
                elif b == 0x9B and self.utf8_pending == 0:   # C1 CSI
                    self._flush_text()
                    self._csi_start()
                elif b == 0x9C and self.utf8_pending == 0:   # C1 ST
                    self._flush_text()
                else:
                    self._text_byte(b)
            elif state == S_ESC:
                self._handle_esc(b)
            elif state == S_CSI:
                if 0x30 <= b <= 0x3F:
                    self.csi_params.append(b)
                elif 0x20 <= b <= 0x2F:
                    self.csi_inter.append(b)
                elif 0x40 <= b <= 0x7E:
                    self.state = S_GROUND
                    try:
                        self._dispatch_csi(bytes(self.csi_params),
                                           bytes(self.csi_inter), b)
                    except Exception:
                        pass               # never let one bad sequence kill the run
                elif b == 0x1B:
                    self.state = S_ESC
                elif b == 0x7F:
                    pass                   # ignore
                elif b < 0x20:
                    self._exec_control(b)  # controls execute inside CSI too
                else:
                    # malformed: abandon CSI and reinterpret this byte
                    self.state = S_GROUND
                    i -= 1
            elif state == S_OSC:
                if b == 0x07 or b == 0x9C:
                    self.state = S_GROUND
                elif b == 0x1B:
                    self.state = S_OSC_ESC
                # payload (e.g. window title) is deliberately ignored
            elif state == S_OSC_ESC:
                if b == 0x5C:              # ESC \
                    self.state = S_GROUND
                elif b == 0x1B:
                    pass
                else:
                    self.state = S_OSC
            elif state == S_STR:           # DCS / PM / APC: skip to ST or BEL
                if b == 0x1B:
                    self.state = S_OSC_ESC
                elif b == 0x07 or b == 0x9C:
                    self.state = S_GROUND
            elif state == S_CHARSET:       # ESC ( B, ESC ) 0, ESC # 8, ...
                self.state = S_GROUND

    def finalize(self):
        """Flush decoder state at end of stream (may emit U+FFFD)."""
        self._flush_text()
        try:
            tail = self.decoder.decode(b"", True)
        except Exception:
            tail = ""
        if tail:
            self._emit_text(tail)
        # an escape sequence truncated at EOF is simply dropped
        self._reset_parser()

    def _csi_start(self):
        self.state = S_CSI
        self.csi_params = bytearray()
        self.csi_inter = bytearray()

    def _exec_control(self, b):
        if b in (0x0A, 0x0B, 0x0C):        # LF, VT, FF
            self.linefeed()
        elif b == 0x0D:                    # CR
            self.buf.x = 0
            self.buf.wrap_pending = False
        elif b == 0x08:                    # BS
            self.backspace()
        elif b == 0x09:                    # HT
            self.tab()
        # BEL (0x07), SO/SI, NUL etc. are ignored

    def _handle_esc(self, b):
        if b == 0x1B:
            self.state = S_ESC
            return
        if b == 0x5B:                      # '['
            self._csi_start()
            return
        if b == 0x5D:                      # ']' OSC
            self.state = S_OSC
            return
        if b in (0x28, 0x29, 0x2A, 0x2B, 0x2D, 0x2E, 0x2F, 0x23):
            # charset designators, ESC # ... : consume one byte, no-op
            self.state = S_CHARSET
            return
        if b in (0x50, 0x5E, 0x5F, 0x58):  # DCS, PM, APC, SOS
            self.state = S_STR
            return
        self.state = S_GROUND
        if b < 0x20:
            self._exec_control(b)
        elif b == 0x37:                    # ESC 7 - DECSC
            self.save_cursor()
        elif b == 0x38:                    # ESC 8 - DECRC
            self.restore_cursor()
        elif b == 0x44:                    # ESC D - IND
            self.linefeed()
        elif b == 0x45:                    # ESC E - NEL
            self.buf.x = 0
            self.linefeed()
        elif b == 0x4D:                    # ESC M - RI
            self.reverse_index()
        elif b == 0x63:                    # ESC c - RIS
            self.hard_reset()
        # anything else: unknown 2-byte escape -> the byte was consumed, no-op

    # -- basic operations --------------------------------------------------

    def _blank(self):
        c = Cell()
        c.bg = self.pen.bg
        return c

    def _blank_row(self):
        return [self._blank() for _ in range(self.cols)]

    def hard_reset(self):
        self.pen = Pen()
        b = self.buf
        b.grid = [[Cell() for _ in range(self.cols)] for _ in range(self.rows)]
        b.x = b.y = 0
        b.top = 0
        b.bottom = self.rows - 1
        b.autowrap = True
        b.wrap_pending = False
        b.cursor_visible = True
        b.saved = None

    def clear_buffer(self, buf):
        buf.grid = [[Cell() for _ in range(self.cols)] for _ in range(self.rows)]

    def resize(self, cols, rows):
        cols = max(1, int(cols))
        rows = max(1, int(rows))
        if cols == self.cols and rows == self.rows:
            return
        self.cols, self.rows = cols, rows
        self.main.resize(cols, rows)
        self.alt.resize(cols, rows)

    def linefeed(self):
        b = self.buf
        b.wrap_pending = False
        if b.y == b.bottom:
            self.scroll_up(1)
        else:
            b.y = min(self.rows - 1, b.y + 1)

    def reverse_index(self):
        b = self.buf
        b.wrap_pending = False
        if b.y == b.top:
            self.scroll_down(1)
        else:
            b.y = max(0, b.y - 1)

    def backspace(self):
        b = self.buf
        b.wrap_pending = False
        if b.x > 0:
            b.x -= 1

    def tab(self):
        b = self.buf
        b.wrap_pending = False
        nxt = ((b.x // 8) + 1) * 8
        if nxt >= self.cols:
            nxt = self.cols - 1
        b.x = max(b.x, nxt) if nxt > b.x else self.cols - 1

    def back_tab(self):
        b = self.buf
        b.wrap_pending = False
        if b.x == 0:
            return
        nxt = ((b.x - 1) // 8) * 8
        b.x = max(0, nxt)

    def scroll_up(self, n):
        b = self.buf
        height = b.bottom - b.top + 1
        n = clamp(int(n), 0, height)
        if n <= 0:
            return
        for y in range(b.top, b.bottom - n + 1):
            b.grid[y] = b.grid[y + n]
        for y in range(b.bottom - n + 1, b.bottom + 1):
            b.grid[y] = self._blank_row()

    def scroll_down(self, n):
        b = self.buf
        height = b.bottom - b.top + 1
        n = clamp(int(n), 0, height)
        if n <= 0:
            return
        for y in range(b.bottom, b.top + n - 1, -1):
            b.grid[y] = b.grid[y - n]
        for y in range(b.top, b.top + n):
            b.grid[y] = self._blank_row()

    def _fix_wide(self, row, start):
        """Remove orphaned wide-char halves around a splice point."""
        if start < 0 or start >= self.cols:
            return
        cells = self.buf.grid[row]
        c = cells[start]
        if c.cont and start > 0:
            cells[start - 1] = self._blank()
            c = self._blank()
            cells[start] = c
        elif c.width == 2 and start + 1 < self.cols:
            cells[start + 1] = self._blank()

    # -- writing -----------------------------------------------------------

    def put_char(self, ch, width):
        b = self.buf
        if width == 0:
            # combining mark: draw over the previous cell
            idx = self.cols - 1 if b.wrap_pending else b.x - 1
            idx = clamp(idx, 0, self.cols - 1)
            cell = b.grid[b.y][idx]
            if cell.cont and idx > 0:
                idx -= 1
                cell = b.grid[b.y][idx]
            if cell.comb is None:
                cell.comb = []
            cell.comb.append(ch)
            return

        if b.wrap_pending and b.autowrap:
            b.wrap_pending = False
            b.x = 0
            self.linefeed()

        if width == 2:
            if self.cols < 2:
                width = 1
            elif b.x >= self.cols - 1:
                if b.autowrap:
                    b.x = 0
                    self.linefeed()
                else:
                    b.x = self.cols - 2

        row = b.grid[b.y]
        cell = row[b.x]
        cell.ch = ch
        cell.comb = None
        cell.fg = self.pen.fg
        cell.bg = self.pen.bg
        cell.attr = self.pen.attr
        cell.width = width
        cell.cont = False
        if width == 2 and b.x + 1 < self.cols:
            cont = row[b.x + 1]
            cont.ch = ""
            cont.comb = None
            cont.fg = self.pen.fg
            cont.bg = self.pen.bg
            cont.attr = self.pen.attr
            cont.width = 0
            cont.cont = True
        b.last_char = ch
        b.last_width = width

        b.x += width
        if b.x >= self.cols:
            b.x = self.cols - 1
            b.wrap_pending = b.autowrap        # no pending wrap when ?7l

    # -- erase / insert / delete ------------------------------------------

    def _erase_cells(self, row, x0, x1):
        x0 = clamp(x0, 0, self.cols - 1)
        x1 = clamp(x1, 0, self.cols - 1)
        if x1 < x0:
            return
        cells = self.buf.grid[row]
        for x in range(x0, x1 + 1):
            cells[x] = self._blank()

    def erase_in_line(self, mode):
        b = self.buf
        if mode == 0:
            self._erase_cells(b.y, b.x, self.cols - 1)
        elif mode == 1:
            self._erase_cells(b.y, 0, b.x)
        elif mode == 2:
            self._erase_cells(b.y, 0, self.cols - 1)

    def erase_in_display(self, mode):
        b = self.buf
        if mode == 0:
            self._erase_cells(b.y, b.x, self.cols - 1)
            for y in range(b.y + 1, self.rows):
                self._erase_cells(y, 0, self.cols - 1)
        elif mode == 1:
            for y in range(0, b.y):
                self._erase_cells(y, 0, self.cols - 1)
            self._erase_cells(b.y, 0, b.x)
        elif mode in (2, 3):               # 3 = scrollback, which we do not keep
            for y in range(self.rows):
                self._erase_cells(y, 0, self.cols - 1)

    def erase_chars(self, n):
        b = self.buf
        if n <= 0:
            return
        self._erase_cells(b.y, b.x, min(self.cols - 1, b.x + n - 1))

    def insert_chars(self, n):
        b = self.buf
        if n <= 0 or b.x >= self.cols:
            return
        row = b.grid[b.y]
        n = min(n, self.cols - b.x)
        for x in range(self.cols - 1, b.x + n - 1, -1):
            row[x] = row[x - n]
        for x in range(b.x, b.x + n):
            row[x] = self._blank()
        self._fix_wide(b.y, b.x)
        self._fix_wide(b.y, self.cols - 1)

    def delete_chars(self, n):
        b = self.buf
        if n <= 0 or b.x >= self.cols:
            return
        row = b.grid[b.y]
        n = min(n, self.cols - b.x)
        for x in range(b.x, self.cols - n):
            row[x] = row[x + n]
        for x in range(self.cols - n, self.cols):
            row[x] = self._blank()
        self._fix_wide(b.y, b.x)

    def insert_lines(self, n):
        b = self.buf
        if n <= 0 or not (b.top <= b.y <= b.bottom):
            return
        n = min(n, b.bottom - b.y + 1)
        for y in range(b.bottom, b.y + n - 1, -1):
            b.grid[y] = b.grid[y - n]
        for y in range(b.y, b.y + n):
            b.grid[y] = self._blank_row()
        b.x = 0

    def delete_lines(self, n):
        b = self.buf
        if n <= 0 or not (b.top <= b.y <= b.bottom):
            return
        n = min(n, b.bottom - b.y + 1)
        for y in range(b.y, b.bottom - n + 1):
            b.grid[y] = b.grid[y + n]
        for y in range(b.bottom - n + 1, b.bottom + 1):
            b.grid[y] = self._blank_row()
        b.x = 0

    # -- cursor / modes ----------------------------------------------------

    def save_cursor(self):
        b = self.buf
        b.saved = (b.x, b.y, self.pen.fg, self.pen.bg, self.pen.attr,
                   b.autowrap)

    def restore_cursor(self):
        b = self.buf
        if not b.saved:
            b.x = b.y = 0
            return
        x, y, fg, bg, attr, autowrap = b.saved
        b.x = clamp(x, 0, self.cols - 1)
        b.y = clamp(y, 0, self.rows - 1)
        b.autowrap = autowrap
        b.wrap_pending = False
        self.pen.fg, self.pen.bg, self.pen.attr = fg, bg, attr

    def set_alt_screen(self, enable, mode=1049):
        if enable:
            if self.using_alt:
                return
            if mode == 1049:
                self.save_cursor()
            self.buf = self.alt
            self.using_alt = True
            if mode in (1047, 1049):
                self.clear_buffer(self.alt)
                self.alt.x = self.alt.y = 0
                self.alt.top = 0
                self.alt.bottom = self.rows - 1
                self.alt.wrap_pending = False
        else:
            if not self.using_alt:
                return
            if mode == 1047:
                self.clear_buffer(self.alt)
            self.buf = self.main
            self.using_alt = False
            if mode == 1049:
                self.restore_cursor()

    # -- CSI ---------------------------------------------------------------

    @staticmethod
    def _parse_params(raw):
        s = raw.decode("latin-1")
        private = ""
        if s and s[0] in "?<=>":
            private = s[0]
            s = s[1:]
        if s == "":
            return private, []
        chunks = []
        for part in s.split(";"):
            sub = []
            for value in part.split(":"):
                if value == "":
                    sub.append(None)
                else:
                    try:
                        sub.append(int(value))
                    except ValueError:
                        sub.append(None)
            chunks.append(sub)
        return private, chunks

    @staticmethod
    def _p(chunks, index, default):
        if index < len(chunks):
            sub = chunks[index]
            if sub and sub[0] is not None:
                return sub[0]
        return default

    def _dispatch_csi(self, raw, inter, final):
        private, chunks = self._parse_params(raw)
        c = chr(final)
        b = self.buf

        if c == "m" and not private:
            self._sgr(chunks)
            return
        if c in "hl" and private:
            self._private_modes(chunks, c == "h")
            return
        if private:
            return                          # unknown private: ignore
        if inter:
            return                          # e.g. CSI ! p (DECSTR): ignore

        # Any cursor movement / screen edit cancels a pending deferred wrap
        # (SGR and mode queries deliberately do not).
        if c in _WRAP_CANCEL:
            b.wrap_pending = False

        if c == "A":                        # CUU
            n = self._p(chunks, 0, 1)
            floor = b.top if b.y >= b.top else 0
            b.y = max(floor, b.y - max(1, n))
        elif c == "B":                      # CUD
            n = self._p(chunks, 0, 1)
            ceil = b.bottom if b.y <= b.bottom else self.rows - 1
            b.y = min(ceil, b.y + max(1, n))
        elif c == "C":                      # CUF
            b.x = min(self.cols - 1, b.x + max(1, self._p(chunks, 0, 1)))
        elif c == "D":                      # CUB
            b.x = max(0, b.x - max(1, self._p(chunks, 0, 1)))
        elif c == "E":                      # CNL
            n = max(1, self._p(chunks, 0, 1))
            ceil = b.bottom if b.y <= b.bottom else self.rows - 1
            b.y = min(ceil, b.y + n)
            b.x = 0
        elif c == "F":                      # CPL
            n = max(1, self._p(chunks, 0, 1))
            floor = b.top if b.y >= b.top else 0
            b.y = max(floor, b.y - n)
            b.x = 0
        elif c == "G" or c == "`":          # CHA / HPA
            b.x = clamp(self._p(chunks, 0, 1) - 1, 0, self.cols - 1)
        elif c in ("H", "f"):               # CUP / HVP
            b.y = clamp(self._p(chunks, 0, 1) - 1, 0, self.rows - 1)
            b.x = clamp(self._p(chunks, 1, 1) - 1, 0, self.cols - 1)
        elif c == "d":                      # VPA
            b.y = clamp(self._p(chunks, 0, 1) - 1, 0, self.rows - 1)
        elif c == "I":                      # CHT
            for _ in range(clamp(self._p(chunks, 0, 1), 0, self.cols)):
                self.tab()
        elif c == "Z":                      # CBT
            for _ in range(clamp(self._p(chunks, 0, 1), 0, self.cols)):
                self.back_tab()
        elif c == "J":                      # ED
            self.erase_in_display(self._p(chunks, 0, 0))
        elif c == "K":                      # EL
            self.erase_in_line(self._p(chunks, 0, 0))
        elif c == "L":                      # IL
            self.insert_lines(max(1, self._p(chunks, 0, 1)))
        elif c == "M":                      # DL
            self.delete_lines(max(1, self._p(chunks, 0, 1)))
        elif c == "@":                      # ICH
            self.insert_chars(max(1, self._p(chunks, 0, 1)))
        elif c == "P":                      # DCH
            self.delete_chars(max(1, self._p(chunks, 0, 1)))
        elif c == "X":                      # ECH
            self.erase_chars(max(1, self._p(chunks, 0, 1)))
        elif c == "S":                      # SU
            self.scroll_up(max(1, self._p(chunks, 0, 1)))
        elif c == "T":                      # SD
            self.scroll_down(max(1, self._p(chunks, 0, 1)))
        elif c == "b":                      # REP
            n = clamp(self._p(chunks, 0, 1), 0, self.cols * self.rows)
            for _ in range(n):
                self.put_char(b.last_char, b.last_width)
        elif c == "r":                      # DECSTBM
            top = self._p(chunks, 0, 1)
            bottom = self._p(chunks, 1, self.rows)
            top = clamp(top, 1, self.rows)
            bottom = clamp(bottom, 1, self.rows)
            if top < bottom:
                b.top = top - 1
                b.bottom = bottom - 1
            else:
                b.top = 0
                b.bottom = self.rows - 1
            b.x = 0
            b.y = 0                       # xterm homes to the screen origin
        elif c == "s":                      # SCOSC
            self.save_cursor()
        elif c == "u":                      # SCORC
            self.restore_cursor()
        elif c == "t":                      # window ops: honour resize only
            if self._p(chunks, 0, 0) == 8:
                rows = self._p(chunks, 1, self.rows)
                cols = self._p(chunks, 2, self.cols)
                if 1 <= cols <= 1000 and 1 <= rows <= 1000:
                    self.resize(cols, rows)
        # everything else (n, c, q, g, p, ...) is deliberately ignored

    def _private_modes(self, chunks, enable):
        for sub in chunks:
            if not sub or sub[0] is None:
                continue
            mode = sub[0]
            if mode == 25:
                self.buf.cursor_visible = enable
            elif mode == 7:
                self.buf.autowrap = enable
                if not enable:
                    self.buf.wrap_pending = False
            elif mode in (47, 1047, 1049):
                self.set_alt_screen(enable, mode)
            # 1 (DECCKM), 12, 1000/1002/1003/1005/1006/1015 (mouse),
            # 2004 (bracketed paste), 1048, ... are ignored on purpose

    # -- SGR ---------------------------------------------------------------

    def _sgr_reset(self):
        self.pen.fg = None
        self.pen.bg = None
        self.pen.attr = 0

    def _set_fg_index(self, idx):
        if 0 <= idx < 256:
            self.pen.fg = PALETTE256[idx]

    def _set_bg_index(self, idx):
        if 0 <= idx < 256:
            self.pen.bg = PALETTE256[idx]

    def _ext_color_colon(self, base, sub):
        """Handle 38:.../48:... (colon separated sub-parameters)."""
        if not sub:
            return
        mode = sub[0]
        values = [v for v in sub[1:] if v is not None]
        if mode == 5 and values:
            if base == 38:
                self._set_fg_index(values[0])
            else:
                self._set_bg_index(values[0])
        elif mode == 2 and len(values) >= 3:
            rgb = tuple(clamp(v, 0, 255) for v in values[-3:])
            if base == 38:
                self.pen.fg = rgb
            else:
                self.pen.bg = rgb

    def _ext_color_flat(self, base, chunks, i):
        """Handle 38;5;N / 38;2;R;G;B. Returns number of chunks consumed."""
        if i >= len(chunks):
            return 0
        mode = chunks[i][0] if chunks[i] else None
        if mode == 5:
            if i + 1 < len(chunks) and chunks[i + 1] and chunks[i + 1][0] is not None:
                if base == 38:
                    self._set_fg_index(chunks[i + 1][0])
                else:
                    self._set_bg_index(chunks[i + 1][0])
            return 2
        if mode == 2:
            vals = []
            for k in range(i + 1, min(i + 4, len(chunks))):
                vals.append(chunks[k][0] if chunks[k] and chunks[k][0] is not None else 0)
            while len(vals) < 3:
                vals.append(0)
            rgb = tuple(clamp(v, 0, 255) for v in vals[:3])
            if base == 38:
                self.pen.fg = rgb
            else:
                self.pen.bg = rgb
            return 4
        return 1

    def _sgr(self, chunks):
        if not chunks:
            self._sgr_reset()
            return
        i = 0
        n = len(chunks)
        while i < n:
            sub = chunks[i]
            code = sub[0] if (sub and sub[0] is not None) else 0
            if code in (38, 48):
                if len(sub) > 1:
                    self._ext_color_colon(code, sub[1:])
                    i += 1
                else:
                    consumed = self._ext_color_flat(code, chunks, i + 1)
                    i += 1 + max(1, consumed)
                continue
            if code == 0:
                self._sgr_reset()
            elif code == 1:
                self.pen.attr |= A_BOLD
            elif code == 2:
                self.pen.attr |= A_DIM
            elif code == 3:
                self.pen.attr |= A_ITALIC
            elif code == 4:                # 4 / 4:n (underline styles)
                self.pen.attr |= A_UNDERLINE
            elif code == 7:
                self.pen.attr |= A_REVERSE
            elif code == 9:
                self.pen.attr |= A_STRIKE
            elif code == 21:
                self.pen.attr &= ~A_BOLD
            elif code == 22:
                self.pen.attr &= ~(A_BOLD | A_DIM)
            elif code == 23:
                self.pen.attr &= ~A_ITALIC
            elif code == 24:
                self.pen.attr &= ~A_UNDERLINE
            elif code == 27:
                self.pen.attr &= ~A_REVERSE
            elif code == 29:
                self.pen.attr &= ~A_STRIKE
            elif 30 <= code <= 37:
                self._set_fg_index(code - 30)
            elif code == 39:
                self.pen.fg = None
            elif 40 <= code <= 47:
                self._set_bg_index(code - 40)
            elif code == 49:
                self.pen.bg = None
            elif 90 <= code <= 97:
                self._set_fg_index(code - 90 + 8)
            elif 100 <= code <= 107:
                self._set_bg_index(code - 100 + 8)
            # 5/6 (blink), 8 (hidden), 25/28, 53/55, 58/59 ... ignored
            i += 1

    # -- text dump ---------------------------------------------------------

    def screen_lines(self):
        out = []
        for row in self.buf.grid:
            parts = []
            for cell in row:
                if cell.cont:
                    continue
                ch = cell.ch if cell.ch else " "
                if cell.comb:
                    ch += "".join(cell.comb)
                parts.append(ch)
            out.append("".join(parts).rstrip())
        return out

    def dump_text(self):
        return "\n".join(self.screen_lines())


# --------------------------------------------------------------------------
# rendering
# --------------------------------------------------------------------------

_FONT_CACHE = {}


def load_font(path, size):
    """Load a TTF at the given size; returns None if it cannot be loaded."""
    if not path:
        return None
    key = (path, size)
    if key in _FONT_CACHE:
        return _FONT_CACHE[key]
    try:
        font = ImageFont.truetype(path, size)
    except Exception:
        font = None
    _FONT_CACHE[key] = font
    return font


def font_variant(path, kind):
    """Return e.g. DejaVuSansMono-Bold.ttf next to DejaVuSansMono.ttf if present."""
    if not path:
        return None
    root, ext = os.path.splitext(path)
    cand = root + "-" + kind + ext
    return cand if os.path.exists(cand) else None


def resolve_font_path(requested):
    if requested:
        if os.path.exists(requested):
            return requested
        print("ansipng: warning: font %r not found" % requested, file=sys.stderr)
    for cand in FALLBACK_FONTS:
        if os.path.exists(cand):
            return cand
    for cand in ("/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf",):
        if os.path.exists(cand):
            return cand
    return None


# direction specs for box drawing: lowercase = light, uppercase = heavy
_BOX_SPEC_TEXT = """
2500 lr
2501 LR
2502 ud
2503 UD
250C dr
250D dR
250E Dr
250F DR
2510 dl
2511 dL
2512 Dl
2513 DL
2514 ur
2515 uR
2516 Ur
2517 UR
2518 ul
2519 uL
251A Ul
251B UL
251C udr
251D udR
251E Udr
251F uDr
2520 UDr
2521 UdR
2522 uDR
2523 UDR
2524 udl
2525 udL
2526 Udl
2527 uDl
2528 UDl
2529 UdL
252A uDL
252B UDL
252C lrd
252D Lrd
252E lRd
252F LRd
2530 lrD
2531 LrD
2532 lRD
2533 LRD
2534 lru
2535 Lru
2536 lRu
2537 LRu
2538 lrU
2539 LrU
253A lRU
253B LRU
253C lrud
253D Lrud
253E lRud
253F LRud
2540 lrUd
2541 lruD
2542 lrUD
2543 LrUd
2544 lRUd
2545 LruD
2546 lRuD
2547 LRUd
2548 LRuD
2549 LrUD
254A lRUD
254B LRUD
256D arc-dr
256E arc-dl
256F arc-ul
2570 arc-ur
2574 l
2575 u
2576 r
2577 d
2578 L
2579 U
257A R
257B D
257C lR
257D uD
257E Lr
257F Ud
"""


def _parse_box_specs():
    table = {}
    for line in _BOX_SPEC_TEXT.strip().splitlines():
        line = line.strip()
        if not line:
            continue
        cp_text, spec = line.split(None, 1)
        table[int(cp_text, 16)] = spec.split()
    return table


BOX_SPECS = _parse_box_specs()

# dashed line variants: code point -> (dashes, heavy)
DASH_SPECS = {
    0x2504: (3, False), 0x2505: (3, True),
    0x2506: (3, False), 0x2507: (3, True),
    0x2508: (4, False), 0x2509: (4, True),
    0x250A: (4, False), 0x250B: (4, True),
    0x254C: (2, False), 0x254D: (2, True),
    0x254E: (2, False), 0x254F: (2, True),
}

SHADES = {0x2591: 0.25, 0x2592: 0.50, 0x2593: 0.75}

# (col, row) quadrant occupancy, 0 = left/top, 1 = right/bottom
QUADRANTS = {
    0x2596: ((0, 1),),
    0x2597: ((1, 1),),
    0x2598: ((0, 0),),
    0x2599: ((0, 0), (0, 1), (1, 1)),
    0x259A: ((0, 0), (1, 1)),
    0x259B: ((0, 0), (1, 0), (0, 1)),
    0x259C: ((0, 0), (1, 0), (1, 1)),
    0x259D: ((1, 0),),
    0x259E: ((1, 0), (0, 1)),
    0x259F: ((1, 0), (0, 1), (1, 1)),
}

BRAILLE_DOTS = (
    (0, 0, 0x01), (0, 1, 0x02), (0, 2, 0x04), (1, 0, 0x08),
    (1, 1, 0x10), (1, 2, 0x20), (0, 3, 0x40), (1, 3, 0x80),
)

# CSI final bytes that move the cursor or edit the screen; they cancel the
# deferred (pending) autowrap, exactly like a real terminal.
_WRAP_CANCEL = frozenset("ABCDEFGHIJKLMPSTXZ@`dfru")


def _box(draw, x0, y0, x1, y1, color):
    """Fill the pixel box [x0,x1) x [y0,y1) (floats, edges exclusive)."""
    ix0 = _r(min(x0, x1))
    iy0 = _r(min(y0, y1))
    ix1 = _r(max(x0, x1)) - 1
    iy1 = _r(max(y0, y1)) - 1
    if ix1 < ix0:
        ix1 = ix0
    if iy1 < iy0:
        iy1 = iy0
    draw.rectangle([ix0, iy0, ix1, iy1], fill=color)


MAX_PIXELS = 64 * 1000 * 1000      # sanity limit for a single preview image


class RenderSizeError(Exception):
    """Raised when the requested image would be unreasonably large."""


class Renderer(object):
    def __init__(self, term, font_path=DEFAULT_FONT, cell_w=DEFAULT_CELL_W,
                 cell_h=DEFAULT_CELL_H, scale=DEFAULT_SCALE,
                 bg=(0x10, 0x12, 0x16), fg=(0xc8, 0xcc, 0xd4)):
        self.term = term
        self.scale = max(0.1, float(scale))
        self.cw = max(1, _r(cell_w * self.scale))
        self.ch = max(1, _r(cell_h * self.scale))
        self.bg = bg
        self.fg = fg
        self.unit = max(1, _r(self.scale))       # 1px at scale 1
        self.bold_offset = max(1, _r(self.scale))

        size = max(1, int(self.ch * 0.86))
        self.font_path = font_path
        self.bold_path = font_variant(font_path, "Bold")
        self.italic_path = font_variant(font_path, "Oblique")
        self.bolditalic_path = font_variant(font_path, "BoldOblique")
        self.font = self._load(size, font_path)
        if self.font is None:
            try:
                self.font = ImageFont.load_default()
            except Exception:
                self.font = None
        self.bold_font = self._load(size, self.bold_path)
        self.italic_font = self._load(size, self.italic_path)
        self.bolditalic_font = self._load(size, self.bolditalic_path)

    @staticmethod
    def _load(size, path):
        """Load one font file; None means 'not available' (fake-bold fallback)."""
        if not path:
            return None
        return load_font(path, size)

    # -- top level ---------------------------------------------------------

    def render(self):
        cols, rows = self.term.cols, self.term.rows
        width, height = cols * self.cw, rows * self.ch
        if width * height > MAX_PIXELS:
            raise RenderSizeError(
                "refusing to render %dx%d px (%.1f MP); lower --scale, "
                "--cell-w/--cell-h, -w or -r" % (width, height,
                                                 width * height / 1e6))
        img = Image.new("RGB", (width, height), self.bg)
        draw = ImageDraw.Draw(img)
        grid = self.term.buf.grid
        for row in range(rows):
            gy = row * self.ch
            for col in range(cols):
                cell = grid[row][col]
                if cell.cont:
                    continue              # painted by its base cell
                gx = col * self.cw
                wide = cell.width == 2 and col + 1 < cols
                w = self.cw * (2 if wide else 1)

                fg = cell.fg if cell.fg is not None else self.fg
                bg = cell.bg if cell.bg is not None else self.bg
                attr = cell.attr
                if attr & A_REVERSE:
                    fg, bg = bg, fg
                if attr & A_DIM:
                    fg = dim_color(fg)

                draw.rectangle([gx, gy, gx + w - 1, gy + self.ch - 1], fill=bg)

                ch = cell.ch
                if ch and ch != " ":
                    try:
                        self._draw_glyph(draw, ch, cell, gx, gy, w, fg, bg, attr)
                    except Exception:
                        pass              # a glyph must never break the render
                if attr & A_UNDERLINE:
                    t = self.unit
                    _box(draw, gx, gy + self.ch - t, gx + w, gy + self.ch, fg)
                if attr & A_STRIKE:
                    t = self.unit
                    mid = gy + self.ch / 2.0
                    _box(draw, gx, mid - t / 2.0, gx + w, mid + t / 2.0, fg)
        return img

    # -- glyph dispatch ----------------------------------------------------

    def _draw_glyph(self, draw, ch, cell, x, y, w, fg, bg, attr):
        cp = ord(ch)
        if 0x2800 <= cp <= 0x28FF:
            self._draw_braille(draw, cp, x, y, fg)
            return
        if self._draw_block(draw, cp, x, y, w, fg, bg):
            return
        if self._draw_box_drawing(draw, cp, x, y, fg):
            return
        self._draw_text(draw, ch, cell, x, y, w, fg, attr)

    # -- braille -----------------------------------------------------------

    def _draw_braille(self, draw, cp, x, y, color):
        bits = cp - 0x2800
        if not bits:
            return
        slot_w = self.cw / 2.0
        slot_h = self.ch / 4.0
        d = min(slot_w, slot_h) * 0.85
        rad = d / 2.0
        for dcol, drow, bit in BRAILLE_DOTS:
            if bits & bit:
                cx = x + dcol * slot_w + slot_w / 2.0
                cy = y + drow * slot_h + slot_h / 2.0
                draw.ellipse([cx - rad, cy - rad, cx + rad, cy + rad], fill=color)

    # -- block elements ----------------------------------------------------

    def _draw_block(self, draw, cp, x, y, w, fg, bg):
        cw, ch = self.cw, self.ch
        if cp == 0x2588:                                  # full block
            _box(draw, x, y, x + cw, y + ch, fg)
        elif cp == 0x2580:                                # upper half
            _box(draw, x, y, x + cw, y + ch / 2.0, fg)
        elif cp == 0x2584:                                # lower half
            _box(draw, x, y + ch / 2.0, x + cw, y + ch, fg)
        elif cp == 0x258C:                                # left half
            _box(draw, x, y, x + cw / 2.0, y + ch, fg)
        elif cp == 0x2590:                                # right half
            _box(draw, x + cw / 2.0, y, x + cw, y + ch, fg)
        elif 0x2581 <= cp <= 0x2587:                      # lower 1/8 .. 7/8
            h = ch * (cp - 0x2580) / 8.0
            _box(draw, x, y + ch - h, x + cw, y + ch, fg)
        elif 0x2589 <= cp <= 0x258F:                      # left 7/8 .. 1/8
            bw = cw * (0x2590 - cp) / 8.0
            _box(draw, x, y, x + bw, y + ch, fg)
        elif cp == 0x2594:                                # upper one eighth
            _box(draw, x, y, x + cw, y + ch / 8.0, fg)
        elif cp == 0x2595:                                # right one eighth
            _box(draw, x + cw - cw / 8.0, y, x + cw, y + ch, fg)
        elif cp in SHADES:
            _box(draw, x, y, x + cw, y + ch, blend(fg, bg, SHADES[cp]))
        elif cp in QUADRANTS:
            for qcol, qrow in QUADRANTS[cp]:
                _box(draw, x + qcol * cw / 2.0, y + qrow * ch / 2.0,
                     x + (qcol + 1) * cw / 2.0, y + (qrow + 1) * ch / 2.0, fg)
        else:
            return False
        return True

    # -- box drawing -------------------------------------------------------

    def _hline(self, draw, xa, xb, yc, t, color):
        _box(draw, xa, yc - t / 2.0, xb, yc + t / 2.0, color)

    def _vline(self, draw, ya, yb, xc, t, color):
        _box(draw, xc - t / 2.0, ya, xc + t / 2.0, yb, color)

    def _dashes_h(self, draw, xa, xb, yc, t, color, count):
        span = (xb - xa) / float(2 * count - 1)
        for i in range(count):
            start = xa + i * 2 * span
            self._hline(draw, start, start + span, yc, t, color)

    def _dashes_v(self, draw, ya, yb, xc, t, color, count):
        span = (yb - ya) / float(2 * count - 1)
        for i in range(count):
            start = ya + i * 2 * span
            self._vline(draw, start, start + span, xc, t, color)

    def _draw_arc_corner(self, draw, kind, x, y, t, color):
        bbox = [x, y, x + self.cw - 1, y + self.ch - 1]
        spans = {"dr": (0, 90), "dl": (90, 180), "ul": (180, 270), "ur": (270, 360)}
        start, end = spans[kind]
        draw.arc(bbox, start, end, fill=color, width=max(1, t))

    def _draw_double(self, draw, cp, x, y, t, color):
        """Double-line box drawing characters (approximation)."""
        cw, ch = self.cw, self.ch
        xm, ym = x + cw / 2.0, y + ch / 2.0
        xe, ye = x + cw, y + ch
        d = max(1.0, t * 1.6)
        h = lambda xa, xb, yc: self._hline(draw, xa, xb, yc, t, color)
        v = lambda ya, yb, xc: self._vline(draw, ya, yb, xc, t, color)
        if cp == 0x2550:
            h(x, xe, ym - d); h(x, xe, ym + d)
        elif cp == 0x2551:
            v(y, ye, xm - d); v(y, ye, xm + d)
        elif cp == 0x2554:                                # ╔
            h(xm - d, xe, ym - d); h(xm + d, xe, ym + d)
            v(ym - d, ye, xm - d); v(ym + d, ye, xm + d)
        elif cp == 0x2557:                                # ╗
            h(x, xm + d, ym - d); h(x, xm - d, ym + d)
            v(ym - d, ye, xm + d); v(ym + d, ye, xm - d)
        elif cp == 0x255A:                                # ╚
            h(xm - d, xe, ym + d); h(xm + d, xe, ym - d)
            v(y, ym + d, xm - d); v(y, ym - d, xm + d)
        elif cp == 0x255D:                                # ╝
            h(x, xm + d, ym + d); h(x, xm - d, ym - d)
            v(y, ym + d, xm + d); v(y, ym - d, xm - d)
        elif cp == 0x2560:                                # ╠
            v(y, ye, xm - d); v(y, ye, xm + d)
            h(xm - d, xe, ym - d); h(xm + d, xe, ym + d)
        elif cp == 0x2563:                                # ╣
            v(y, ye, xm - d); v(y, ye, xm + d)
            h(x, xm + d, ym - d); h(x, xm - d, ym + d)
        elif cp == 0x2566:                                # ╦
            h(x, xe, ym - d); h(x, xe, ym + d)
            v(ym - d, ye, xm - d); v(ym + d, ye, xm + d)
        elif cp == 0x2569:                                # ╩
            h(x, xe, ym - d); h(x, xe, ym + d)
            v(y, ym + d, xm - d); v(y, ym - d, xm + d)
        elif cp == 0x256C:                                # ╬
            h(x, xe, ym - d); h(x, xe, ym + d)
            v(y, ye, xm - d); v(y, ye, xm + d)
        else:
            return False
        return True

    def _draw_box_drawing(self, draw, cp, x, y, fg):
        cw, ch = self.cw, self.ch
        xm, ym = x + cw / 2.0, y + ch / 2.0
        xe, ye = x + cw, y + ch
        t1 = float(self.unit)                  # light
        t2 = float(max(2, self.unit * 2))      # heavy

        if cp in DASH_SPECS:
            count, heavy = DASH_SPECS[cp]
            t = t2 if heavy else t1
            if cp in (0x2504, 0x2505, 0x2508, 0x2509, 0x254C, 0x254D):
                self._dashes_h(draw, x, xe, ym, t, fg, count)
            else:
                self._dashes_v(draw, y, ye, xm, t, fg, count)
            return True
        if cp in BOX_SPECS:
            for token in BOX_SPECS[cp]:
                if token.startswith("arc-"):
                    self._draw_arc_corner(draw, token[4:], x, y, _r(t1), fg)
                    continue
                for kind in token:
                    t = t2 if kind.isupper() else t1
                    if kind in ("l", "L"):
                        self._hline(draw, x, xm, ym, t, fg)
                    elif kind in ("r", "R"):
                        self._hline(draw, xm, xe, ym, t, fg)
                    elif kind in ("u", "U"):
                        self._vline(draw, y, ym, xm, t, fg)
                    elif kind in ("d", "D"):
                        self._vline(draw, ym, ye, xm, t, fg)
            return True
        if 0x2550 <= cp <= 0x256C:
            return self._draw_double(draw, cp, x, y, _r(t1), fg)
        return False

    # -- text --------------------------------------------------------------

    def _font_for(self, attr):
        if attr & A_BOLD and attr & A_ITALIC and self.bolditalic_font:
            return self.bolditalic_font
        if attr & A_BOLD and self.bold_font:
            return self.bold_font
        if attr & A_ITALIC and self.italic_font:
            return self.italic_font
        return self.font

    def _draw_text(self, draw, ch, cell, x, y, w, fg, attr):
        font = self._font_for(attr)
        if font is None:
            _box(draw, x + w * 0.15, y + self.ch * 0.15,
                 x + w * 0.85, y + self.ch * 0.85, fg)
            return
        text = ch + ("".join(cell.comb) if cell.comb else "")
        try:
            bbox = font.getbbox(ch)
        except Exception:
            bbox = (0, 0, w, self.ch)
        if bbox is None:
            bbox = (0, 0, w, self.ch)
        try:
            advance = font.getlength(ch)
        except Exception:
            advance = float(w)
        try:
            ascent, descent = font.getmetrics()
        except Exception:
            ascent, descent = self.ch, 0
        if ascent + descent <= 0:
            ascent, descent = self.ch, 0

        bx0 = bbox[0]
        tx = x + (w - advance) / 2.0 - bx0
        ty = y + (self.ch - (ascent + descent)) / 2.0

        try:
            mask_box = font.getmask(ch).getbbox()
        except Exception:
            mask_box = (0, 0, 1, 1)
        if mask_box is None and ch.strip():
            # glyph missing from the font: draw "tofu"
            _box(draw, x + w * 0.12, y + self.ch * 0.16,
                 x + w * 0.88, y + self.ch * 0.84, fg)
            return

        draw.text((tx, ty), text, font=font, fill=fg)
        if (attr & A_BOLD) and not self.bold_font:
            off = self.bold_offset
            draw.text((tx + off, ty), text, font=font, fill=fg)


# --------------------------------------------------------------------------
# self test
# --------------------------------------------------------------------------

def build_self_test_stream(cols=DEFAULT_COLS, rows=DEFAULT_ROWS):
    """Build a synthetic capture that exercises the features of this tool."""
    E = "\x1b"
    out = []

    def w(s):
        out.append(s)

    def at(row, col=1):
        w("%s[%d;%dH" % (E, row, col))

    # --- alternate screen must not leak into the final image -------------
    w("%s[2J%s[H" % (E, E))
    w("%s[?1049h" % E)
    w("%s[1;1HTRANSIENT ALT SCREEN BUFFER -- must not be visible" % E)
    w("%s[?1049l" % E)

    # --- title bar (reverse video) ---------------------------------------
    title = " ansipng self-test :: TUI-CHART verification render "
    if len(title) < cols:
        title = title + " " * (cols - len(title))
    at(1)
    w("%s[7m%s%s[0m" % (E, title[:cols], E))

    # --- truecolor + 256 colour swatches ---------------------------------
    at(2)
    w("%s[1mtruecolor:%s[0m " % (E, E))
    for i in range(24):
        r = int(255 * i / 23.0)
        g = int(255 * (23 - i) / 23.0)
        b = 128
        w("%s[48;2;%d;%d;%dm " % (E, r, g, b))
    w("%s[0m" % E)

    at(3)
    w("%s[1m256-colour:%s[0m " % (E, E))
    for i in range(16, 40):
        w("%s[48;5;%dm " % (E, i))
    w("%s[0m" % E)
    for i in range(232, 256):
        w("%s[48;5;%dm " % (E, i))
    w("%s[0m" % E)

    # --- bordered panel, box drawing, rounded + double + heavy -----------
    at(5)
    w("%s[38;5;39m\u250c" % E + "\u2500" * 34 + "\u252c" + "\u2500" * 30 + "\u2510" + ("%s[0m" % E))
    labels = [
        ("PANEL A", "box drawing: \u250c \u2510 \u2514 \u2518 \u251c \u2524 \u2534 \u253c"),
        ("TEE/HEAVY", "heavy: \u2501\u2501 \u2503 \u250f\u2513\u2517\u251b \u2505\u2505\u2505"),
        ("ROUNDED", "rounded: \u256d\u2500\u256e \u2570\u2500\u256f and double \u2554\u2550\u2557"),
        ("WIDE CHARS", "\u4e2d\u6587\u5b57 wide: \u65e5\u672c\u8a9e \u30c6\u30b9\u30c8 ok"),
    ]
    for i, (left, right) in enumerate(labels):
        at(6 + i)
        w("%s[38;5;39m\u2502%s[0m " % (E, E))
        w("%-16s" % left[:16])
        w("%s[38;5;39m\u2502%s[0m " % (E, E))
        w("%-30s" % right[:30])
        w("%s[38;5;39m\u2502%s[0m" % (E, E))
    at(6 + len(labels))
    w("%s[38;5;39m\u251c" % E + "\u2500" * 34 + "\u253c" + "\u2500" * 30 + "\u2524" + ("%s[0m" % E))
    at(7 + len(labels))
    w("%s[38;5;39m\u2514" % E + "\u2500" * 34 + "\u2534" + "\u2500" * 30 + "\u2518" + ("%s[0m" % E))

    # --- braille line chart ----------------------------------------------
    base_row = 13
    at(base_row - 1)
    w("braille chart:")
    patterns = [
        (0x01 | 0x08, "28"), (0x03 | 0x18, "34"), (0x07 | 0x38, "40"),
        (0x0F | 0x78, "46"), (0x1F | 0x08, "82"), (0x3F | 0x18, "118"),
        (0x7F | 0x38, "154"), (0x7F | 0x78, "190"), (0x3F | 0x78, "226"),
        (0x1F | 0x38, "220"), (0x0F | 0x18, "214"), (0x07 | 0x08, "208"),
    ]
    for r in range(6):
        at(base_row + r)
        line = []
        for i in range(28):
            bits = patterns[(i + r) % len(patterns)][0]
            color = patterns[(i + r) % len(patterns)][1]
            line.append("%s[38;5;%sm%s" % (E, color, chr(0x2800 + (bits & 0xFF))))
        w("".join(line) + "%s[0m" % E)

    # --- eighth blocks bar chart -----------------------------------------
    at(base_row + 7)
    w("eighth blocks: ")
    for i in range(40):
        h = 1 + ((i * 7) % 8)
        w("%s[38;5;%dm%s" % (E, 33 + (i % 6), chr(0x2580 + h)))
    w("%s[0m" % E)
    at(base_row + 8)
    w("shades/quadrants: ")
    w("\u2591\u2592\u2593\u2588 \u2596\u2597\u2598\u2599\u259a\u259b\u259c\u259d\u259e\u259f \u2581\u2582\u2583\u2584\u2585\u2586\u2587 \u2589\u258a\u258b\u258c\u258d\u258e\u258f \u2590\u2580\u2594\u2595")
    w("%s[0m" % E)

    # --- attributes -------------------------------------------------------
    at(base_row + 10)
    w("%s[1mbold%s[0m %s[2mdim%s[0m %s[3mitalic%s[0m %s[4munderline%s[0m "
      "%s[7mreverse%s[0m %s[9mstrike%s[0m %s[1;4;38;2;255;170;0mbold+underline+truecolor%s[0m"
      % (E, E, E, E, E, E, E, E, E, E, E, E, E, E))

    # --- erase + cursor addressing ---------------------------------------
    at(base_row + 12)
    w("erase tests: ##########%s[10D%s[K" % (E, E))          # EL 0
    at(base_row + 13)
    w("wrap/autowrap: " + "abcdefghij" * 3)

    # --- a truncated escape sequence at EOF must not break anything ------
    w("%s[38;5" % E)
    return "".join(out).encode("utf-8")


def run_self_test(args, bg, fg):
    cols, rows = args.cols, args.rows
    data = build_self_test_stream(cols, rows)
    out_path = args.output or os.path.join(
        os.path.dirname(os.path.abspath(__file__)), "self-test.png")

    term = Terminal(cols, rows)
    warning = None
    try:
        term.feed(data)
        term.finalize()
    except Exception as exc:                       # pragma: no cover - defensive
        warning = "parse error (continuing): %r" % (exc,)

    renderer = Renderer(term, font_path=resolve_font_path(args.font),
                        cell_w=args.cell_w, cell_h=args.cell_h,
                        scale=args.scale, bg=bg, fg=fg)
    try:
        img = renderer.render()
    except RenderSizeError as exc:
        print("ansipng self-test: FAIL: %s" % exc, file=sys.stderr)
        return 2
    save_image(img, out_path)

    text = term.dump_text()
    sys.stdout.write(text + "\n")

    problems = []
    if warning:
        problems.append(warning)
    if not os.path.exists(out_path) or os.path.getsize(out_path) == 0:
        problems.append("PNG was not written")
    colors = img.getcolors(maxcolors=1 << 24) or []
    if len(colors) <= 2:
        problems.append("image has only %d distinct colours" % len(colors))
    if "self-test" not in text:
        problems.append("title bar text missing from the screen")
    if "ALT SCREEN" in text:
        problems.append("alternate screen buffer leaked into the main screen")
    if "\u28ff" not in text and not any("\u2800" <= c <= "\u28ff" for c in text):
        problems.append("braille content missing")
    if "\u2500" not in text:
        problems.append("box drawing content missing")

    print("ansipng self-test: %dx%d cells, %dx%d px, %d distinct colours, %s"
          % (cols, rows, img.size[0], img.size[1], len(colors), out_path),
          file=sys.stderr)
    if problems:
        for p in problems:
            print("ansipng self-test: FAIL: %s" % p, file=sys.stderr)
        return 1
    print("SELF-TEST OK")
    return 0


# --------------------------------------------------------------------------
# CLI
# --------------------------------------------------------------------------

def save_image(img, path):
    try:
        img.save(path)
    except Exception:
        # e.g. an output path without a recognisable extension
        img.save(path, format="PNG")


def derive_output_path(input_path):
    root, ext = os.path.splitext(input_path)
    if ext.lower() == ".png":
        return root + ".render.png"
    return root + ".png"


def build_arg_parser():
    p = argparse.ArgumentParser(
        prog="ansipng",
        description="Render captured terminal output (ANSI/VT bytes) to a PNG.",
        epilog="If OUTPUT is omitted it is derived from INPUT with a .png suffix.")
    p.add_argument("input", nargs="?", help="file with raw captured bytes")
    p.add_argument("output", nargs="?", help="output PNG path")
    p.add_argument("-w", "--cols", type=int, default=DEFAULT_COLS,
                   help="terminal width in columns (default %d)" % DEFAULT_COLS)
    p.add_argument("-r", "--rows", type=int, default=DEFAULT_ROWS,
                   help="terminal height in rows (default %d)" % DEFAULT_ROWS)
    p.add_argument("--font", default=DEFAULT_FONT,
                   help="TTF font path (default %s)" % DEFAULT_FONT)
    p.add_argument("--cell-w", type=int, default=DEFAULT_CELL_W,
                   help="cell width in px (default %d)" % DEFAULT_CELL_W)
    p.add_argument("--cell-h", type=int, default=DEFAULT_CELL_H,
                   help="cell height in px (default %d)" % DEFAULT_CELL_H)
    p.add_argument("--scale", type=float, default=DEFAULT_SCALE,
                   help="render scale multiplier (default %s)" % DEFAULT_SCALE)
    p.add_argument("--bg", default=DEFAULT_BG,
                   help="default background RRGGBB (default %s)" % DEFAULT_BG)
    p.add_argument("--fg", default=DEFAULT_FG,
                   help="default foreground RRGGBB (default %s)" % DEFAULT_FG)
    p.add_argument("--dump-text", action="store_true",
                   help="also print the final screen as plain text to stdout")
    p.add_argument("--self-test", action="store_true",
                   help="render a synthetic stream that exercises every feature")
    p.add_argument("--version", action="version", version="ansipng " + VERSION)
    return p


def main(argv=None):
    args = build_arg_parser().parse_args(argv)
    bg = parse_color(args.bg, (0x10, 0x12, 0x16))
    fg = parse_color(args.fg, (0xc8, 0xcc, 0xd4))
    if bg is None:
        bg = (0x10, 0x12, 0x16)
    if fg is None:
        fg = (0xc8, 0xcc, 0xd4)

    if args.self_test:
        # With --self-test there is no INPUT file, so a single positional
        # argument is the OUTPUT path.
        if args.output is None and args.input is not None:
            args.output, args.input = args.input, None
        return run_self_test(args, bg, fg)

    if not args.input:
        print("ansipng: error: INPUT is required (or use --self-test)",
              file=sys.stderr)
        return 2
    if not os.path.exists(args.input):
        print("ansipng: error: no such file: %s" % args.input, file=sys.stderr)
        return 2

    try:
        with open(args.input, "rb") as fh:
            data = fh.read()
    except OSError as exc:
        print("ansipng: error: cannot read %s: %s" % (args.input, exc),
              file=sys.stderr)
        return 2

    out_path = args.output or derive_output_path(args.input)

    term = Terminal(args.cols, args.rows)
    parse_error = None
    try:
        term.feed(data)
        term.finalize()
    except Exception as exc:
        # Never lose the render because of malformed input.
        parse_error = exc
        print("ansipng: warning: parse error, rendering partial screen: %r"
              % (exc,), file=sys.stderr)

    renderer = Renderer(term, font_path=resolve_font_path(args.font),
                        cell_w=args.cell_w, cell_h=args.cell_h,
                        scale=args.scale, bg=bg, fg=fg)
    try:
        img = renderer.render()
    except RenderSizeError as exc:
        print("ansipng: error: %s" % exc, file=sys.stderr)
        return 2
    save_image(img, out_path)

    if args.dump_text:
        sys.stdout.write(term.dump_text() + "\n")

    colors = img.getcolors(maxcolors=1 << 24) or []
    print("ansipng: wrote %s (%dx%d px, %d cells, %d colours)%s"
          % (out_path, img.size[0], img.size[1], term.cols * term.rows,
             len(colors), " [partial: parse error]" if parse_error else ""),
          file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
