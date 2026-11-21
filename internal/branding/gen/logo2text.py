#!/usr/bin/env python3
"""Derive the text-art variants of the Datamedrix logo from the official PNG.

Usage (from anywhere):

    python3 internal/branding/gen/logo2text.py [path/to/dmx_logo.png] [--all DIR]

Writes internal/branding/logo_wide.txt and internal/branding/logo_compact.txt
and prints them. With --all DIR it also writes every candidate variant
(logo_wide_6.txt, logo_wide_8.txt, ...) to DIR. Requires Pillow. The output
is deterministic.

Method (grid fitting, as font hinting does it)
----------------------------------------------
* The alpha channel of the PNG is the ink mask of the word mark DATAMEDRIX.
  It is split into its ten letters by connected components; each letter
  keeps only its own ink (the slanted A legs reach into D and T).
* The mark is drawn on a 5-module grid: E, the second D, R, I and X are
  built from 35-pixel modules; D, A, T and M use 1.5-module bars and
  1.7-module stems. HINTS below lists, per letter, the PNG coordinates of
  its stems, bars and counters. They are checked against the PNG (each one
  must be a real ink edge), so the hints cannot drift from the logo.
* Each hint segment gets a whole number of sub-pixels in the target grid:
  stems a fixed stroke width (thick or thin, the same for every letter), bars
  a fixed bar height, the rest ("flex") shares what is left. The letter is
  then warped piecewise-linearly so every hinted edge lands exactly on a
  sub-pixel boundary, and area-sampled (coverage >= 50 % is ink). Diagonals
  (A, M) and the D's rounded corners come from the sampling; straight
  strokes come out crisp and equally thick in every letter.
* A sub-pixel is half a cell wide and half a cell high, so every 2 x 2
  sub-pixels become one Unicode quadrant block (U+2580..U+259F). Quadrant
  blocks are in every common terminal font (unlike sextants), which matters
  over SSH and on the Linux console.
* Terminal cells are about 1:2, so a 14:1 word mark at 60 columns would be
  only about three rows high. The mark is stretched vertically; the variants
  differ in how much.

The resulting variants still require the owner's approval (spec 7.1, A14).
"""
import os
import sys

from PIL import Image

QUAD = {
    (0, 0, 0, 0): " ", (1, 0, 0, 0): "▘", (0, 1, 0, 0): "▝", (1, 1, 0, 0): "▀",
    (0, 0, 1, 0): "▖", (1, 0, 1, 0): "▌", (0, 1, 1, 0): "▞", (1, 1, 1, 0): "▛",
    (0, 0, 0, 1): "▗", (1, 0, 0, 1): "▚", (0, 1, 0, 1): "▐", (1, 1, 0, 1): "▜",
    (0, 0, 1, 1): "▄", (1, 0, 1, 1): "▙", (0, 1, 1, 1): "▟", (1, 1, 1, 1): "█",
}

LETTERS = "DATAMEDRIX"
MODULES = 5

# Per-letter hints, in PNG pixels relative to the letter's bounding box
# (x) and to the ink's top edge (y). Each list covers the whole letter as
# consecutive segments (end, kind). Kinds:
#   x: "thick"/"thin" stem, "sq" pixel square (thin), "gap" (0 wide), "flex"
#   y: "bar" (1.5-module bar), "mod" (one module), "flex"
# Letter boxes in the 2500 x 179 PNG: D 0-251, A 243-512, T 478-692,
# A 663-932, M 948-1236, E 1285-1531, D 1568-1814, R 1851-2097, I 2132-2167,
# X 2203-2500; ink rows 2-176 (174 px = 5 modules of ~35 px).
MOD_Y = [(35, "mod"), (70, "mod"), (104, "mod"), (139, "mod"), (174, "mod")]
HINTS = {
    0: dict(x=[(64, "thick"), (186, "flex"), (251, "thick")],
            y=[(51, "bar"), (123, "flex"), (174, "bar")]),
    1: dict(sym=True, x=[(99, "flex"), (169, "ctr"), (269, "flex")], y=[(117, "flex"), (151, "mod"), (174, "flex")]),
    2: dict(sym=True, x=[(71, "flex"), (142, "stem"), (214, "flex")],
            y=[(51, "bar"), (174, "flex")]),
    3: dict(sym=True, x=[(99, "flex"), (169, "ctr"), (269, "flex")], y=[(117, "flex"), (151, "mod"), (174, "flex")]),
    4: dict(sym=True, x=[(59, "thick"), (229, "flex"), (288, "thick")], y=[(174, "flex")]),
    5: dict(x=[(59, "thick"), (176, "flex"), (246, "flex")], y=MOD_Y),
    6: dict(x=[(37, "thin"), (210, "flex"), (246, "thin")], y=MOD_Y),
    7: dict(x=[(35, "thin"), (175, "flex"), (211, "thin"), (246, "thin")], y=MOD_Y),
    8: dict(x=[(35, "thin")], y=MOD_Y),
    9: dict(x=[(35, "sq"), (44, "gap"), (79, "sq"), (87, "gap"), (123, "sq"),
               (131, "gap"), (166, "sq"), (175, "gap"), (210, "sq"), (218, "gap"),
               (253, "sq"), (262, "gap"), (297, "sq")], y=MOD_Y),
}

# Candidate variants. width: columns; mods: sub-pixel rows per module (top
# to bottom); bar: sub-pixel rows of a 1.5-module bar; thick/thin/stem:
# sub-pixel columns of a thick stem, thin stem and the T's centred stem;
# sq: sub-pixel columns of an X square; gap: sub-pixel columns between
# letters; align: letters a whole number of cells wide; widths: explicit
# sub-pixel width per letter instead of the proportional share.
VARIANTS = {
    "wide_6": dict(width=60, mods=[2, 3, 2, 3, 2], bar=3, thick=2, thin=2, stem=2, ctr=4,
                   sq=2, gap=2, align=True),
    "wide_8": dict(width=60, mods=[3] * 5, bar=4, thick=2, thin=2, stem=2, ctr=4,
                   sq=2, gap=2, align=True),
    "wide_10": dict(width=60, mods=[4] * 5, bar=6, thick=2, thin=2, stem=2, ctr=4,
                    sq=2, gap=2, align=True),
    "compact_3": dict(width=30, mods=[1] * 5, bar=1, thick=1, thin=1, stem=2, ctr=2,
                      sq=1, gap=2, align=True, widths=[4, 4, 4, 4, 4, 4, 4, 4, 1, 7]),
    "compact_4": dict(width=30, mods=[1, 2, 1, 2, 1], bar=2, thick=1, thin=1, stem=2, ctr=2,
                      sq=1, gap=2, align=True, widths=[4, 4, 4, 4, 4, 4, 4, 4, 1, 7]),
    "compact_5": dict(width=30, mods=[2] * 5, bar=3, thick=1, thin=1, stem=2, ctr=2,
                      sq=1, gap=2, align=True, widths=[4, 4, 4, 4, 4, 4, 4, 4, 1, 7]),
}
INSTALL = {"wide": "wide_6", "compact": "compact_3"}


def ink_mask(path):
    alpha = Image.open(path).convert("RGBA").getchannel("A")
    return alpha.crop(alpha.getbbox())


def components(mask, level=128):
    """Connected components: list of (x0, x1, pixel list), by x0."""
    w, h = mask.size
    px = mask.load()
    seen = bytearray(w * h)
    comps = []
    for x0 in range(w):
        for y0 in range(h):
            if px[x0, y0] < level or seen[y0 * w + x0]:
                continue
            seen[y0 * w + x0] = 1
            stack, pts = [(x0, y0)], []
            while stack:
                x, y = stack.pop()
                pts.append((x, y))
                for q in ((x + 1, y), (x - 1, y), (x, y + 1), (x, y - 1)):
                    if 0 <= q[0] < w and 0 <= q[1] < h and not seen[q[1] * w + q[0]] \
                            and px[q] >= level:
                        seen[q[1] * w + q[0]] = 1
                        stack.append(q)
            xs = [p[0] for p in pts]
            comps.append((min(xs), max(xs) + 1, pts))
    comps.sort(key=lambda c: c[0])
    return comps


def letters(mask):
    """Ten letter images, each holding only its own components' ink. A
    pixel square touching a letter belongs to it; the separate squares after
    the ninth letter make up the X."""
    groups = []
    for c in components(mask):
        if groups and c[0] - groups[-1][1] <= 2 and c[1] - c[0] < 60:
            g = groups[-1]  # a pixel square touching the letter (D, R)
            groups[-1] = (g[0], max(g[1], c[1]), g[2] + [c])
        else:
            groups.append((c[0], c[1], [c]))
    while len(groups) > len(LETTERS):  # the X's separate squares
        a, b = groups[-2], groups[-1]
        groups[-2:] = [(a[0], max(a[1], b[1]), a[2] + b[2])]
    assert len(groups) == len(LETTERS), [(g[0], g[1]) for g in groups]
    h = mask.size[1]
    src = mask.load()
    out = []
    for x0, x1, comps in groups:
        img = Image.new("L", (x1 - x0, h), 0)
        dst = img.load()
        for c in comps:
            for x, y in c[2]:
                dst[x - x0, y] = src[x, y]
        out.append(img)
    return out


def check_hints(imgs):
    """Every hinted x edge must be a column where the amount of ink changes,
    every hinted y edge a row where it changes (or the letter's border)."""
    mod_edges = {}
    for i, img in enumerate(imgs):
        w, h = img.size
        p = img.load()
        hint = HINTS[i]
        assert hint["x"][-1][0] == w, (LETTERS[i], hint["x"][-1][0], w)
        assert hint["y"][-1][0] == h, (LETTERS[i], hint["y"][-1][0], h)
        col = [sum(p[x, y] >= 128 for y in range(h)) for x in range(w)]
        row = [sum(p[x, y] >= 128 for x in range(w)) for y in range(h)]
        has_ctr = any(kind == "ctr" for _, kind in hint["x"])
        for e, _ in hint["x"][:-1]:
            if has_ctr:
                # A counter edge: an ink/space boundary in the row just
                # above the crossbar.
                y = hint["y"][0][0] - 2
                d = 255 * h if any(
                    (p[x - 1, y] >= 128) != (p[x, y] >= 128) for x in range(e - 2, e + 3)) else 0
            else:
                d = max(abs(col[min(w - 1, e + k)] - col[max(0, e - 1 - k)]) for k in range(3))
            assert d >= 0.1 * h, ("x hint is no edge", LETTERS[i], e, d)
        for e, _ in hint["y"][:-1]:
            d = max(abs(row[min(h - 1, e + k)] - row[max(0, e - 1 - k)]) for k in range(3))
            if hint["y"] is MOD_Y:
                mod_edges[e] = mod_edges.get(e, False) or d >= 0.1 * w
            else:
                assert d >= 0.1 * w, ("y hint is no edge", LETTERS[i], e, d)
    # The module grid is shared; each of its lines must be an edge somewhere.
    assert all(mod_edges.values()), mod_edges


def fit(segments, total, fixed):
    """Target sizes for consecutive source segments [(end, kind), ...]:
    fixed kinds get fixed[kind]; "flex" segments share the rest in
    proportion to their source size (largest remainder)."""
    starts = [0] + [e for e, _ in segments[:-1]]
    sizes = [None] * len(segments)
    flex = []
    for k, ((e, kind), s) in enumerate(zip(segments, starts)):
        if kind in fixed:
            sizes[k] = fixed[kind]
        else:
            flex.append((k, e - s))
    rest = total - sum(v for v in sizes if v is not None)
    assert rest >= 0 and (rest == 0 or flex), (segments, total)
    src = sum(n for _, n in flex)
    exact = [(k, rest * n / src) for k, n in flex]
    for k, v in exact:
        sizes[k] = int(v)
    left = rest - sum(sizes[k] for k, _ in flex)
    for k, v in sorted(exact, key=lambda kv: -(kv[1] - int(kv[1])))[:left]:
        sizes[k] += 1
    return sizes


def knots(segments, sizes):
    """Piecewise-linear map as [(src, dst), ...] knot pairs."""
    out, s, d = [(0, 0)], 0, 0
    for (e, _), n in zip(segments, sizes):
        s, d = e, d + n
        out.append((s, d))
    return out


def src_range(kn, t0, t1):
    """Source interval of the target cell [t0, t1)."""
    def inv(t, right):
        cands = [(a, b) for a, b in zip(kn, kn[1:]) if a[1] != b[1] and
                 (a[1] <= t < b[1] if right else a[1] < t <= b[1])]
        (s0, d0), (s1, d1) = cands[0]
        return s0 + (t - d0) * (s1 - s0) / (d1 - d0)
    return inv(t0, True), inv(t1, False)


def coverage(sat, w, x0, x1, y0, y1):
    """Mean ink (0..1) over a fractional source rectangle, via a
    summed-area table on a 4x supersampled grid."""
    def S(x, y):
        return sat[int(round(y * 4)) * (w * 4 + 1) + int(round(x * 4))]
    area = (x1 - x0) * (y1 - y0) * 16
    if area <= 0:
        return 0.0
    v = S(x1, y1) - S(x0, y1) - S(x1, y0) + S(x0, y0)
    return v / 255 / area


def sat_of(img):
    w, h = img.size
    big = img.resize((w * 4, h * 4), Image.NEAREST).load()
    W, H = w * 4 + 1, h * 4 + 1
    sat = [0] * (W * H)
    for y in range(1, H):
        run = 0
        for x in range(1, W):
            run += big[x - 1, y - 1]
            sat[y * W + x] = sat[(y - 1) * W + x] + run
    return sat


def letter_widths(imgs, cfg):
    """Sub-pixel width per letter: the I and X are fixed by their strokes;
    the other letters share the rest in proportion to their PNG width. With
    cfg["align"] every letter is a whole number of cells wide, so letters
    and gaps start on cell boundaries."""
    unit = 2 if cfg.get("align") else 1
    total = (cfg["width"] * 2 - cfg["gap"] * (len(imgs) - 1)) // unit
    fixed = {8: -(-cfg["thin"] // unit), 9: -(-7 * cfg["sq"] // unit)}
    segs = []
    end = 0
    for i, img in enumerate(imgs):
        end += img.size[0]
        segs.append((end, "f%d" % i if i in fixed else "flex"))
    sizes = fit(segs, total, {"f%d" % i: v for i, v in fixed.items()})
    return [n * unit for n in sizes]


def despeckle(g):
    """Cleanup: drop ink sub-pixels with no ink neighbour left, right, above
    or below, and fill holes of one sub-pixel; both are sampling noise on a
    diagonal, not features of the mark."""
    h, w = len(g), len(g[0])
    out = [r[:] for r in g]
    for y in range(h):
        for x in range(w):
            nb = [g[y + dy][x + dx] for dx, dy in ((1, 0), (-1, 0), (0, 1), (0, -1))
                  if 0 <= x + dx < w and 0 <= y + dy < h]
            if g[y][x] and not any(nb):
                out[y][x] = 0
            elif not g[y][x] and len(nb) == 4 and all(nb):
                out[y][x] = 1
    return out


def glyph(img, sat, hint, lw, cfg):
    """One letter as rows of sub-pixels, lw wide and sum(cfg["mods"]) high."""
    w = img.size[0]
    mods = cfg["mods"]
    hsub = sum(mods)
    xs = fit(hint["x"], lw, {"thick": cfg["thick"], "thin": cfg["thin"],
                             "stem": cfg["stem"], "sq": cfg["sq"], "ctr": cfg["ctr"],
                             "gap": 0})
    if hint["y"] is MOD_Y:
        ys = list(mods)
    else:
        ys = fit(hint["y"], hsub, {"bar": cfg["bar"], "mod": round(hsub / MODULES)})
    kx, ky = knots(hint["x"], xs), knots(hint["y"], ys)
    cov = []
    for ty in range(hsub):
        sy0, sy1 = src_range(ky, ty, ty + 1)
        row = []
        for tx in range(lw):
            sx0, sx1 = src_range(kx, tx, tx + 1)
            row.append(coverage(sat, w, sx0, sx1, sy0, sy1))
        cov.append(row)
    if hint.get("sym"):  # A, T and M are mirror-symmetric in the logo
        cov = [[(r[x] + r[lw - 1 - x]) / 2 for x in range(lw)] for r in cov]
    g = [[1 if c >= cfg.get("threshold", 0.5) else 0 for c in r] for r in cov]
    # EDRIX are built from whole modules; there a lone sub-pixel is a
    # pixel square of the mark, not noise.
    return g if hint["y"] is MOD_Y else despeckle(g)


def render(imgs, sats, cfg):
    widths = cfg.get("widths") or letter_widths(imgs, cfg)
    hsub = sum(cfg["mods"])
    grid = [[] for _ in range(hsub + hsub % 2)]
    for i, (img, sat, lw) in enumerate(zip(imgs, sats, widths)):
        g = glyph(img, sat, HINTS[i], lw, cfg)
        for ty, row in enumerate(grid):
            if len(row) % 2 and cfg.get("align"):
                row.append(0)  # start the letter on a cell boundary
            row.extend(g[ty] if ty < hsub else [0] * lw)
            if i < len(imgs) - 1:
                row.extend([0] * cfg["gap"])
    width = len(grid[0])
    if width % 2:
        for row in grid:
            row.append(0)
    lines = []
    for y in range(0, len(grid), 2):
        a, b = grid[y], grid[y + 1]
        lines.append("".join(QUAD[(a[x], a[x + 1], b[x], b[x + 1])]
                             for x in range(0, len(a), 2)).rstrip())
    while lines and not lines[-1]:
        lines.pop()
    return "\n".join(lines) + "\n"


def main():
    root = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))
    args = sys.argv[1:]
    all_dir = None
    if "--all" in args:
        k = args.index("--all")
        all_dir = args[k + 1]
        del args[k:k + 2]
    src = args[0] if args else os.path.join(root, "docs", "dmx_logo.png")
    imgs = letters(ink_mask(src))
    check_hints(imgs)
    sats = [sat_of(img) for img in imgs]
    arts = {name: render(imgs, sats, cfg) for name, cfg in VARIANTS.items()}
    for name, art in arts.items():
        lines = art.splitlines()
        print(f"{name}: {max(len(l) for l in lines)}x{len(lines)}")
        print(art)
        if all_dir:
            with open(os.path.join(all_dir, f"logo_{name}.txt"), "w", encoding="utf-8") as f:
                f.write(art)
    out_dir = os.path.join(root, "internal", "branding")
    for name, variant in INSTALL.items():
        with open(os.path.join(out_dir, f"logo_{name}.txt"), "w", encoding="utf-8") as f:
            f.write(arts[variant])


if __name__ == "__main__":
    main()
