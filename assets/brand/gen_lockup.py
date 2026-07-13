#!/usr/bin/env python3
"""Generate the Ergoz horizontal lockup: Meter mark + blocky ERGOZ wordmark.

Letterforms are hand-built from rects/polygons (square, hard miters, no
curves — per the neo-industrial system) so the SVG has zero font
dependencies and renders identically everywhere, GitHub included.
"""

T = 10   # stroke thickness
W = 32   # letter width
H = 44   # cap height
GAP = 12 # letter gap

BONE, CHARCOAL = "#f0ece4", "#1a1a18"
EMBER, RUST = "#e8562a", "#c4532a"


def rect(x, y, w, h):
    return ("rect", x, y, w, h)


def poly(*pts):
    return ("poly", pts)


# Horizontal bars overlap the vertical spines entirely (start at x=0 /
# span to W) so adjacent rects never leave antialiasing seams.
LETTERS = {
    "E": [rect(0, 0, T, H), rect(0, 0, W, T),
          rect(0, 17, W - 6, T), rect(0, H - T, W, T)],
    "R": [rect(0, 0, T, H), rect(0, 0, W - 4, T), rect(W - T, 0, T, 26),
          rect(0, 16, W - 4, T),
          poly((14, 26), (24, 26), (W, H), (W - T, H))],
    "G": [rect(0, 0, T, H), rect(0, 0, W, T), rect(0, H - T, W, T),
          rect(W - T, 20, T, H - 20), rect(16, 20, W - 16, T)],
    "O": [rect(0, 0, T, H), rect(W - T, 0, T, H),
          rect(0, 0, W, T), rect(0, H - T, W, T)],
    "Z": [rect(0, 0, W, T), rect(0, H - T, W, T),
          poly((W - T, T), (W, T), (T, H - T), (0, H - T))],
}


def letter_svg(ch, ox, oy, fill):
    out = []
    for shape in LETTERS[ch]:
        if shape[0] == "rect":
            _, x, y, w, h = shape
            out.append(f'<rect x="{ox+x}" y="{oy+y}" width="{w}" height="{h}" fill="{fill}"/>')
        else:
            pts = " ".join(f"{ox+x},{oy+y}" for x, y in shape[1])
            out.append(f'<polygon points="{pts}" fill="{fill}"/>')
    return out


def meter_mark(ox, oy, scale, fg, accent):
    bars = [(9, 68, 36), (31, 44, 60), (53, 12, 92), (75, 32, 72)]
    out = [f'<rect x="{ox+x*scale:.1f}" y="{oy+y*scale:.1f}" width="{14*scale:.1f}" height="{h*scale:.1f}" fill="{fg}"/>'
           for x, y, h in bars]
    out.append(f'<rect x="{ox+97*scale:.1f}" y="{oy+56*scale:.1f}" width="{14*scale:.1f}" height="{48*scale:.1f}" fill="{accent}"/>')
    return out


def lockup(fg, accent):
    parts = []
    # Mark at 52px, vertically centered against the 44px caps (y offset -4).
    parts += meter_mark(0, -4, 52 / 120, fg, accent)
    x = 76
    for ch in "ERGOZ":
        parts += letter_svg(ch, x, 0, fg)
        x += W + GAP
    width = x - GAP
    body = "\n  ".join(parts)
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 -6 {width} 56" '
            f'width="{width}" height="56">\n  {body}\n</svg>\n')


def mark(fg, accent):
    bars = "\n  ".join(
        f'<rect x="{x}" y="{y}" width="14" height="{h}" fill="{fg}"/>'
        for x, y, h in [(9, 68, 36), (31, 44, 60), (53, 12, 92), (75, 32, 72)])
    return ('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 120" width="120" height="120">\n'
            f'  {bars}\n  <rect x="97" y="56" width="14" height="48" fill="{accent}"/>\n</svg>\n')


with open("logo-horizontal-dark.svg", "w") as f:
    f.write(lockup(BONE, EMBER))
with open("logo-horizontal-light.svg", "w") as f:
    f.write(lockup(CHARCOAL, RUST))
with open("mark-dark.svg", "w") as f:
    f.write(mark(BONE, EMBER))
with open("mark-light.svg", "w") as f:
    f.write(mark(CHARCOAL, RUST))
print("generated")
