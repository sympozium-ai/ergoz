#!/usr/bin/env python3
"""Generate the Ergoz internal-architecture diagram (agents -> collector).

Neo-industrial system: charcoal/bone/olive, one ember element (the watts
data flow from sysfs/NVML up through the collector's fleet outputs). Square
corners, hard miters, no curves — arrows are polygons.
"""

W, H = 960, 460


def theme(dark):
    if dark:
        return dict(bg="#111110", panel="#1a1a18", ink="#f0ece4",
                    olive="#8a8c82", line="#333330", accent="#e8562a")
    return dict(bg="none", panel="#e9e4da", ink="#1a1a18",
                olive="#8a8c82", line="#c9c3b6", accent="#c4532a")


def esc(s):
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def build(dark):
    c = theme(dark)
    o = []
    MONO = "ui-monospace, 'JetBrains Mono', 'Cascadia Mono', Menlo, monospace"

    def rect(x, y, w, h, fill="none", stroke=None, sw=1.5):
        s = f' stroke="{stroke}" stroke-width="{sw}"' if stroke else ""
        o.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" fill="{fill}"{s}/>')

    def text(x, y, s, size=13, fill=None, weight="normal", ls="0.06em", anchor="start"):
        fill = fill or c["ink"]
        o.append(f'<text x="{x}" y="{y}" font-family="{MONO}" font-size="{size}" '
                 f'font-weight="{weight}" letter-spacing="{ls}" fill="{fill}" '
                 f'text-anchor="{anchor}">{esc(s)}</text>')

    def varrow(x, y0, y1, color, sw=3):
        # vertical segment + triangular head (pointing down if y1>y0)
        o.append(f'<line x1="{x}" y1="{y0}" x2="{x}" y2="{y1}" stroke="{color}" stroke-width="{sw}"/>')
        d = 6 if y1 > y0 else -6
        o.append(f'<polygon points="{x-5},{y1-d} {x+5},{y1-d} {x},{y1}" fill="{color}"/>')

    def harrow(x0, x1, y, color, sw=3):
        o.append(f'<line x1="{x0}" y1="{y}" x2="{x1}" y2="{y}" stroke="{color}" stroke-width="{sw}"/>')
        d = 6 if x1 > x0 else -6
        o.append(f'<polygon points="{x1-d},{y-5} {x1-d},{y+5} {x1},{y}" fill="{color}"/>')

    def sq(x, y, s, fill):
        o.append(f'<rect x="{x}" y="{y}" width="{s}" height="{s}" fill="{fill}"/>')

    if c["bg"] != "none":
        o.append(f'<rect x="0" y="0" width="{W}" height="{H}" fill="{c["bg"]}"/>')

    # frame + corner brackets
    rect(16, 16, W - 32, H - 32, stroke=c["line"], sw=1)
    for cx, cy, dx, dy in [(16, 16, 1, 1), (W - 16, 16, -1, 1), (16, H - 16, 1, -1), (W - 16, H - 16, -1, -1)]:
        o.append(f'<polyline points="{cx+dx*22},{cy} {cx},{cy} {cx},{cy+dy*22}" '
                 f'fill="none" stroke="{c["ink"]}" stroke-width="2"/>')
    text(34, 40, "ERGOZ · NODE AGENTS → FLEET COLLECTOR", 11, c["olive"], ls="0.18em")

    # ── three node/agent boxes (left) ──
    node_x, nw, nh = 44, 340, 96
    ys = [72, 190, 308]
    for i, ny in enumerate(ys):
        rect(node_x, ny, nw, nh, fill=c["panel"], stroke=c["line"])
        text(node_x + 18, ny + 26, f"NODE {i+1}", 12, c["olive"], weight="bold", ls="0.14em")
        text(node_x + 18, ny + 50, "ergoz-agent  ·  DaemonSet", 13, c["ink"], weight="bold")
        text(node_x + 18, ny + 70, "probe → sample → Σ W·Δt", 11, c["olive"])
        # accelerator device squares
        for k in range(3):
            sq(node_x + nw - 96 + k * 26, ny + 34, 16, c["ink"] if k < 2 else c["accent"])
        text(node_x + nw - 96, ny + 82, ":9743/metrics", 10, c["olive"])

    # sysfs/NVML source label under the first node's devices (ember origin)
    text(node_x + nw - 100, ny + nh + 0, "", 10)  # noop keep spacing

    # ── collector box (right) ──
    col_x, cw, ch = 620, 296, 236
    col_y = 112
    rect(col_x, col_y, cw, ch, fill=c["panel"], stroke=c["line"])
    text(col_x + 20, col_y + 30, "ergoz-collector", 15, c["ink"], weight="bold")
    text(col_x + 20, col_y + 50, "Deployment · fleet cache", 11, c["olive"])
    # two output rows
    rect(col_x + 20, col_y + 74, cw - 40, 44, stroke=c["line"], sw=1)
    text(col_x + 34, col_y + 94, ":9744/metrics", 12, c["ink"], weight="bold")
    text(col_x + 34, col_y + 110, "merged Prometheus", 10, c["olive"])
    rect(col_x + 20, col_y + 130, cw - 40, 44, stroke=c["accent"], sw=1.5)
    text(col_x + 34, col_y + 150, ":9744/api/v1/fleet", 12, c["accent"], weight="bold")
    text(col_x + 34, col_y + 166, "JSON → Sympozium", 10, c["olive"])
    text(col_x + 20, col_y + 206, "no CRDs · no RBAC · non-root", 10, c["olive"], ls="0.1em")

    # ── scrape arrows: agents → collector (ember: the telemetry flow) ──
    for ny in ys:
        # from node right edge, elbow into collector left edge
        y = ny + nh // 2
        midx = 590
        o.append(f'<polyline points="{node_x+nw},{y} {midx},{y} {midx},{col_y+ch//2}" '
                 f'fill="none" stroke="{c["accent"]}" stroke-width="2.5"/>')
    harrow(midx, col_x, col_y + ch // 2, c["accent"], sw=3)
    text(470, 60, "SCRAPE / 5s", 10, c["accent"], ls="0.12em", anchor="middle")
    text(470, 74, "HEADLESS-SVC DNS", 9, c["olive"], ls="0.1em", anchor="middle")

    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" '
            f'width="{W}" height="{H}" role="img" '
            f'aria-label="Ergoz architecture: per-node agents scraped by one fleet collector">\n'
            + "\n".join(o) + "\n</svg>\n")


for dark, name in [(True, "arch-dark.svg"), (False, "arch-light.svg")]:
    open(name, "w").write(build(dark))
print("generated")
