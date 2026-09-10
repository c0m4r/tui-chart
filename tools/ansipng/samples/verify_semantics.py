#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Semantics assertions for the ansipng VT emulator (quick regression check).

Run:  python3 verify_semantics.py
Exits 0 and prints "SEMANTICS OK (N checks)" when everything passes.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import ansipng as A  # noqa: E402

E = "\x1b"
CHECKS = []


def check(name, got, want):
    ok = got == want
    CHECKS.append((name, ok, got, want))
    if not ok:
        print("FAIL %-38s got=%r want=%r" % (name, got, want))


def screen(text, cols=20, rows=4, feed_extra=b""):
    t = A.Terminal(cols, rows)
    t.feed(text.encode("utf-8") + feed_extra)
    t.finalize()
    return t


def lines(t):
    return t.screen_lines()


# --- cursor movement / addressing -----------------------------------------
check("CUP row;col", lines(screen("%s[2;3Hab" % E))[1], "  ab")
t = screen("abc%s[2D%s[1BX%s[1A%s[1CY" % (E, E, E, E))
check("CUB+CUD", lines(t)[1], " X")
check("CUU+CUF", lines(t)[0], "abcY")
check("CHA column", lines(screen("abc%s[1GX" % E))[0], "Xbc")
check("CNL/CPL", lines(screen("a%s[2Eb%s[1Fc" % (E, E))), ["a", "c", "b", ""])

# --- erase ----------------------------------------------------------------
check("EL 0", lines(screen("abcdef%s[4D%s[K" % (E, E)))[0], "ab")
check("EL 1", lines(screen("abcdef%s[4D%s[1K" % (E, E)))[0], "   def")
check("EL 2", lines(screen("abcdef%s[2K" % E))[0], "")
check("ED 0", lines(screen("ab\r\ncd%s[1;1H%s[J" % (E, E))), ["", "", "", ""])
check("ED 1", lines(screen("ab\r\ncd%s[2;2H%s[1J" % (E, E))), ["", "", "", ""])
check("ED 1 keeps tail", lines(screen("ab\r\ncd%s[2;1H%s[1J" % (E, E)))[1], " d")
check("ED 2", lines(screen("ab\r\ncd%s[2J" % E)), ["", "", "", ""])

# --- insert / delete ------------------------------------------------------
check("ICH", lines(screen("abcdef%s[1;3H%s[2@" % (E, E)))[0], "ab  cdef")
check("DCH", lines(screen("abcdef%s[1;3H%s[2P" % (E, E)))[0], "abef")
check("ECH", lines(screen("abcdef%s[1;3H%s[2X" % (E, E)))[0], "ab  ef")
check("IL", lines(screen("a\r\nb\r\nc%s[1;1H%s[L" % (E, E))), ["", "a", "b", "c"])
check("DL", lines(screen("a\r\nb\r\nc%s[1;1H%s[M" % (E, E))), ["b", "c", "", ""])
check("SU", lines(screen("a\r\nb\r\nc%s[1S" % E)), ["b", "c", "", ""])
check("SD", lines(screen("a\r\nb%s[2T" % E)), ["", "", "a", "b"])
check("REP", lines(screen("ab%s[3b" % E))[0], "abbbb")

# --- autowrap -------------------------------------------------------------
check("autowrap on",
      lines(screen("abcdef", cols=4, rows=3))[:2], ["abcd", "ef"])
check("autowrap off",
      lines(screen("abcdef%s[?7lgh" % E, cols=4, rows=3))[:2], ["abcd", "efgh"])
check("autowrap off at margin",
      lines(screen("%s[?7labcdefgh" % E, cols=4, rows=3))[:2], ["abch", ""])
check("deferred wrap + CUP",
      lines(screen("abcd%s[1;1HX" % E, cols=4, rows=3)), ["Xbcd", "", ""])
check("scroll at bottom",
      lines(screen("a\r\nb\r\nc\r\nd", cols=4, rows=3)), ["b", "c", "d"])

# --- wide + combining -----------------------------------------------------
t = screen("ab\u4e2dc")
row = t.buf.grid[0]
check("wide char cells", (row[2].ch, row[2].width, row[3].cont), ("\u4e2d", 2, True))
check("wide char cursor", t.buf.x, 5)
t = screen("e\u0301x")
check("combining attaches", t.buf.grid[0][0].comb, ["\u0301"])
check("combining dump", lines(t)[0], "e\u0301x")

# --- tabs / backspace / CR ------------------------------------------------
check("tab stops", lines(screen("a\tb\tc"))[0], "a       b       c")
check("backspace", lines(screen("abc\bX"))[0], "abX")
check("CR overwrite", lines(screen("abc\rX"))[0], "Xbc")

# --- alt screen -----------------------------------------------------------
t = A.Terminal(20, 4)
t.feed(("main%s[?1049halt%s[?1049l" % (E, E)).encode())
check("1049 restores main", lines(t)[0], "main")
t = A.Terminal(20, 4)
t.feed(("main%s[?47halt%s[?47l" % (E, E)).encode())
check("47 restores main", lines(t)[0], "main")
t = A.Terminal(20, 4)
t.feed(("main%s[?1047halt%s[?1047l" % (E, E)).encode())
check("1047 restores main", lines(t)[0], "main")
t = A.Terminal(20, 4)
t.feed(("ab%s[?1049h%s[?1049l" % (E, E)).encode())
check("1049 restores cursor", t.buf.x, 2)

# --- modes / unknown ------------------------------------------------------
check("mouse modes ignored",
      lines(screen("%s[?1000h%s[?1003h%s[?1006h%s[?2004hok" % (E, E, E, E)))[0],
      "ok")
check("unknown private mode", lines(screen("%s[?9999hok" % E))[0], "ok")
check("unknown ESC skips 1 byte", lines(screen("%sZok" % E))[0], "ok")
check("OSC BEL ignored", lines(screen("%s]0;title\x07ok" % E))[0], "ok")
check("OSC ST ignored", lines(screen("%s]0;title%s\\ok" % (E, E)))[0], "ok")
check("charset no-op", lines(screen("%s(0lqk" % E))[0], "lqk")
check("truncated escape at EOF", lines(screen("ok%s[38;5;" % E))[0], "ok")
check("invalid utf-8 replaced", lines(screen("a" + "\ufffd" + "b"))[0], "a\ufffdb")

# --- SGR ------------------------------------------------------------------
t = screen("%s[1;4;38;2;10;20;30;48;5;196mX" % E)
c = t.buf.grid[0][0]
check("truecolor fg", c.fg, (10, 20, 30))
check("256 bg", c.bg, A.PALETTE256[196])
check("bold+underline", (c.attr & A.A_BOLD, c.attr & A.A_UNDERLINE), (1, 8))
t = screen("%s[38:2::1:2:3mX" % E)
check("colon truecolor", t.buf.grid[0][0].fg, (1, 2, 3))
t = screen("%s[38:5:123mX" % E)
check("colon 256", t.buf.grid[0][0].fg, A.PALETTE256[123])
t = screen("%s[1m%s[22mX" % (E, E))
check("SGR 22 clears bold", t.buf.grid[0][0].attr, 0)
t = screen("%s[7m%s[27mX" % (E, E))
check("SGR 27 clears reverse", t.buf.grid[0][0].attr, 0)
check("palette size", len(A.PALETTE256), 256)

# --- scroll region --------------------------------------------------------
t = screen("\r\n".join("l%d" % i for i in range(6)) + "%s[2;4r%s[2;1H%s[M" % (E, E, E),
           cols=10, rows=6)
check("DECSTBM + DL", lines(t)[:5], ["l0", "l2", "l3", "", "l4"])

# --- wcwidth --------------------------------------------------------------
check("wcwidth ascii", A.wcwidth("a"), 1)
check("wcwidth CJK", A.wcwidth("\u4e2d"), 2)
check("wcwidth combining", A.wcwidth("\u0301"), 0)
check("wcwidth braille", A.wcwidth("\u28ff"), 1)

# --- geometry tables ------------------------------------------------------
check("box table size", len(A.BOX_SPECS) >= 76, True)
check("braille bits", A.BRAILLE_DOTS[7], (1, 3, 0x80))
check("quadrant 0x259f", A.QUADRANTS[0x259F], ((1, 0), (0, 1), (1, 1)))

failed = [c for c in CHECKS if not c[1]]
print("%d checks, %d failed" % (len(CHECKS), len(failed)))
if failed:
    for name, _ok, got, want in failed:
        print("  FAILED: %s got=%r want=%r" % (name, got, want))
    sys.exit(1)
print("SEMANTICS OK (%d checks)" % len(CHECKS))
