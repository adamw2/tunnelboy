#!/usr/bin/env python3
"""Render a tmux `capture-pane -e` ANSI text dump to a PNG.

Draws true fixed-width character cells (each glyph is centered in its own
CHAR_W x CHAR_H cell) rather than measuring natural glyph widths — symbols
like the bullets and arrows TunnelBoy's TUI uses (o, *, up/down arrows)
render wider than ASCII in most monospace fonts, and drawing whole strings
at their "expected" width causes visible drift/overlap on later columns.
"""
import argparse
import re
import sys

from PIL import Image, ImageDraw, ImageFont

CHAR_W = 9
CHAR_H = 19
PAD = 16
TITLE_H = 40
BG_DEFAULT = (18, 18, 20)
FG_DEFAULT = (220, 220, 220)

# Tried in order; first one that exists wins. Override with --font.
FONT_CANDIDATES = [
    "/System/Library/Fonts/SFNSMono.ttf",  # macOS
    "/System/Library/Fonts/Menlo.ttc",
    "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf",  # common Linux
    "/usr/share/fonts/truetype/liberation/LiberationMono-Regular.ttf",
]

ANSI_SGR_RE = re.compile(r"\x1b\[([0-9;]*)m")
ANSI_OTHER_RE = re.compile(r"\x1b\[[0-9;?]*[a-zA-Z]")

BASIC_FG = {
    30: (0, 0, 0), 31: (205, 49, 49), 32: (13, 188, 121), 33: (229, 229, 16),
    34: (36, 114, 200), 35: (188, 63, 188), 36: (17, 168, 205), 37: (229, 229, 229),
    90: (102, 102, 102), 91: (241, 76, 76), 92: (35, 209, 139), 93: (245, 245, 67),
    94: (59, 142, 234), 95: (214, 112, 214), 96: (41, 184, 219), 97: (255, 255, 255),
}


def find_font():
    for path in FONT_CANDIDATES:
        try:
            ImageFont.truetype(path, 12)
            return path
        except OSError:
            continue
    return None


def parse_ansi_lines(text):
    """Split into rows of (text, fg, bg, bold) segments, char styling only —
    ignores cursor-movement/erase codes (they don't affect a static capture)."""
    lines = text.split("\n")
    out_lines = []
    fg, bg, bold = FG_DEFAULT, None, False
    for line in lines:
        line = ANSI_OTHER_RE.sub("", line)  # strip non-SGR CSI codes (cursor hide, erase-line, ...)
        segs = []
        pos = 0
        cur_fg, cur_bg, cur_bold = fg, bg, bold
        buf = ""
        for m in ANSI_SGR_RE.finditer(line):
            buf += line[pos:m.start()]
            if buf:
                segs.append((buf, cur_fg, cur_bg, cur_bold))
                buf = ""
            codes = m.group(1).split(";") if m.group(1) else ["0"]
            i = 0
            while i < len(codes):
                c = codes[i]
                if c in ("", "0"):
                    cur_fg, cur_bg, cur_bold = FG_DEFAULT, None, False
                elif c == "1":
                    cur_bold = True
                elif c == "38" and i + 1 < len(codes) and codes[i + 1] == "2":
                    cur_fg = (int(codes[i + 2]), int(codes[i + 3]), int(codes[i + 4]))
                    i += 4
                elif c == "48" and i + 1 < len(codes) and codes[i + 1] == "2":
                    cur_bg = (int(codes[i + 2]), int(codes[i + 3]), int(codes[i + 4]))
                    i += 4
                elif c.isdigit() and int(c) in BASIC_FG:
                    cur_fg = BASIC_FG[int(c)]
                i += 1
            pos = m.end()
        buf += line[pos:]
        if buf:
            segs.append((buf, cur_fg, cur_bg, cur_bold))
        fg, bg, bold = cur_fg, cur_bg, cur_bold
        out_lines.append(segs if segs else [("", FG_DEFAULT, None, False)])
    return out_lines


def render(path_in, path_out, title=None, font_path=None):
    with open(path_in, "r", encoding="utf-8", errors="replace") as f:
        text = f.read()
    lines = parse_ansi_lines(text)
    while lines and all(seg[0].strip() == "" for seg in lines[-1]):
        lines.pop()

    ncols = max((sum(len(s[0]) for s in line) for line in lines), default=80)
    nrows = len(lines)

    title_h = TITLE_H if title else 0
    w = PAD * 2 + ncols * CHAR_W
    h = PAD * 2 + nrows * CHAR_H + title_h

    img = Image.new("RGB", (w, h), BG_DEFAULT)
    draw = ImageDraw.Draw(img)

    font_path = font_path or find_font()
    if font_path:
        font = ImageFont.truetype(font_path, 16)
    else:
        print("warning: no monospace TTF found, falling back to PIL default bitmap font", file=sys.stderr)
        font = ImageFont.load_default()

    y = PAD + title_h
    if title:
        draw.rectangle([0, 0, w, title_h], fill=(45, 45, 48))
        for i, color in enumerate([(237, 106, 94), (245, 191, 79), (97, 197, 86)]):
            cx = 20 + i * 18
            draw.ellipse([cx, title_h // 2 - 6, cx + 12, title_h // 2 + 6], fill=color)
        draw.text((w // 2, title_h // 2), title, fill=(180, 180, 180), font=font, anchor="mm")

    for line in lines:
        x = PAD
        for text_seg, fg, bg, bold in line:
            for ch in text_seg:
                if bg:
                    draw.rectangle([x, y, x + CHAR_W, y + CHAR_H], fill=bg)
                if ch != " ":
                    draw.text((x + CHAR_W / 2, y + CHAR_H / 2), ch, fill=fg, font=font, anchor="mm")
                x += CHAR_W
        y += CHAR_H

    img.save(path_out)
    print(path_out)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("input", help="ANSI text file (tmux capture-pane -e -p output)")
    p.add_argument("output", help="PNG path to write")
    p.add_argument("--title", default=None, help="Window title bar text (adds a fake macOS title bar)")
    p.add_argument("--font", default=None, help="Path to a monospace TTF (default: auto-detect)")
    args = p.parse_args()
    render(args.input, args.output, title=args.title, font_path=args.font)


if __name__ == "__main__":
    main()
