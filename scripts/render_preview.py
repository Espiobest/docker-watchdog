"""Render an exported TUI fixture to SVG using only Python's standard library.

First run TestPreview with WATCHDOG_PREVIEW pointing to an output .ansi file.
Usage: python scripts/render_preview.py artifacts/preview.ansi docs/dashboard.svg
"""

import html
from pathlib import Path
import re
import sys
import unicodedata


def color(index):
    base = ["#000000", "#800000", "#008000", "#808000", "#000080", "#800080",
            "#008080", "#c0c0c0", "#808080", "#ff0000", "#00ff00", "#ffff00",
            "#0000ff", "#ff00ff", "#00ffff", "#ffffff"]
    if index < 16:
        return base[index]
    if index >= 232:
        level = 8 + (index - 232) * 10
        return f"#{level:02x}{level:02x}{level:02x}"
    index -= 16
    levels = [0, 95, 135, 175, 215, 255]
    return "#" + "".join(f"{levels[value]:02x}" for value in (index // 36, index // 6 % 6, index % 6))


def columns(text):
    return sum(0 if unicodedata.combining(char) else 2 if unicodedata.east_asian_width(char) in "WF" else 1 for char in text)


def render(source):
    cell, line_height = 8.5, 23
    lines = source.splitlines()
    width, height = 120 * cell + 40, len(lines) * line_height + 70
    output = [f'<svg xmlns="http://www.w3.org/2000/svg" width="{width:g}" height="{height}" viewBox="0 0 {width:g} {height}">',
              '<title>Docker Watchdog terminal dashboard — illustrative test fixture</title>',
              f'<rect width="{width:g}" height="{height}" rx="12" fill="#111827"/>',
              f'<path d="M0 42H{width:g}" stroke="#263247"/>',
              '<circle cx="22" cy="21" r="5" fill="#f87171"/>',
              '<circle cx="40" cy="21" r="5" fill="#fbbf24"/>',
              '<circle cx="58" cy="21" r="5" fill="#4ade80"/>',
              '<text x="82" y="26" fill="#94a3b8" font-family="monospace" font-size="12">watchdog --label=watchdog.demo=true --auto-restart</text>']
    for row, line in enumerate(lines):
        x, y = 20, 60 + row * line_height
        foreground, background, bold = "#e2e8f0", None, False
        for token in re.split(r"(\x1b\[[0-9;]*m)", line):
            if token.startswith("\x1b["):
                codes = [int(part or 0) for part in token[2:-1].split(";")]
                index = 0
                while index < len(codes):
                    code = codes[index]
                    if code == 0:
                        foreground, background, bold = "#e2e8f0", None, False
                    elif code == 1:
                        bold = True
                    elif code == 22:
                        bold = False
                    elif code in (38, 48) and index + 2 < len(codes) and codes[index + 1] == 5:
                        value = color(codes[index + 2])
                        if code == 38:
                            foreground = value
                        else:
                            background = value
                        index += 2
                    elif code == 39:
                        foreground = "#e2e8f0"
                    elif code == 49:
                        background = None
                    index += 1
                continue
            length = columns(token) * cell
            if background and length:
                output.append(f'<rect x="{x:g}" y="{y - 16}" width="{length:g}" height="{line_height}" fill="{background}"/>')
            if token.strip():
                weight = "bold" if bold else "normal"
                output.append(f'<text x="{x:g}" y="{y}" fill="{foreground}" font-family="Cascadia Code,DejaVu Sans Mono,monospace" font-size="14" font-weight="{weight}" xml:space="preserve" textLength="{length:g}" lengthAdjust="spacingAndGlyphs">{html.escape(token)}</text>')
            x += length
    output.append("</svg>")
    return "\n".join(output) + "\n"


if __name__ == "__main__":
    source, destination = map(Path, sys.argv[1:3])
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text(render(source.read_text(encoding="utf-8")), encoding="utf-8")
