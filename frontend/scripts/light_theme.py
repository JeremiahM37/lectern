"""Generate the light theme's overrides for colours written as literals.

Most of the app's CSS reads the theme tokens (--bg, --panel, --ink, ...), which
theme/app-theme.ts sets for dark or light. Some older rules still name a dark
colour directly. This script finds every such declaration and writes a rule
that applies only in the light theme, mapping each literal onto the token for
the same role: a dark surface becomes the matching light surface, pale text
becomes ink, a white overlay becomes a dark one, and a light accent used as
text is darkened until it reads on white. Saturated fills keep their colour.

Run after changing a stylesheet (npm run build does not):
    python3 frontend/scripts/light_theme.py
Output: frontend/src/theme/light.generated.css (committed).
"""
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parents[1]
WEB = ROOT.parent / "web" / "static"
OUT = ROOT / "src" / "theme" / "light.generated.css"
SOURCES = sorted(p for p in (ROOT / "src").rglob("*.css") if p != OUT) + [
    WEB / name for name in ("style.css", "conversation.css", "workspace.css", "launch-profiles.css", "agent-settings.css", "command-palette.css")
]
LIGHT_BG = (255, 255, 255)
COLOR = re.compile(r"#[0-9a-fA-F]{3,8}\b|rgba?\([^)]*\)")
FALLBACK = re.compile(r"var\(--[\w-]+\s*,[^()]*(?:\([^()]*\)[^()]*)*\)")
TEXT_PROPS = {"color", "fill", "stroke", "caret-color", "text-decoration-color", "-webkit-text-fill-color"}
BORDER_PROPS = re.compile(r"^(border|outline)")
SHADOW_PROPS = {"box-shadow", "text-shadow", "filter"}


def parse(value):
    value = value.strip()
    if value.startswith("#"):
        h = value[1:]
        if len(h) in (3, 4):
            h = "".join(c * 2 for c in h)
        r, g, b = int(h[0:2], 16), int(h[2:4], 16), int(h[4:6], 16)
        a = int(h[6:8], 16) / 255 if len(h) == 8 else 1.0
        return r, g, b, a
    nums = re.findall(r"[\d.]+%?", value)
    r, g, b = (float(n) for n in nums[:3])
    a = 1.0
    if len(nums) > 3:
        a = float(nums[3].rstrip("%")) / (100 if nums[3].endswith("%") else 1)
    return r, g, b, a


def lum(r, g, b):
    def ch(v):
        v /= 255
        return v / 12.92 if v <= 0.03928 else ((v + 0.055) / 1.055) ** 2.4
    return 0.2126 * ch(r) + 0.7152 * ch(g) + 0.0722 * ch(b)


def contrast(a, b):
    x, y = lum(*a), lum(*b)
    return (max(x, y) + 0.05) / (min(x, y) + 0.05)


def saturation(r, g, b):
    # Chroma, not HLS saturation: a near-white like #e2e8f0 has a high HLS
    # saturation but reads as grey.
    return (max(r, g, b) - min(r, g, b)) / 255


def darken_to_read(r, g, b):
    for step in range(0, 21):
        k = 1 - step / 20
        c = (r * k, g * k, b * k)
        if contrast(c, LIGHT_BG) >= 4.6:
            return "#%02x%02x%02x" % tuple(round(v) for v in c)
    return "#000000"


def on_colour(body):
    """Whether the rule paints its own coloured fill (a button, a badge)."""
    for decl in body.split(";"):
        if ":" not in decl:
            continue
        prop, value = decl.split(":", 1)
        if not prop.strip().startswith("background"):
            continue
        if re.search(r"var\(--(accent|red|green|amber|cyan|indigo)", value):
            return True
        for literal in COLOR.findall(value):
            try:
                r, g, b, a = parse(literal)
            except (ValueError, IndexError):
                continue
            if a > 0.5 and saturation(r, g, b) >= 0.25 and lum(r, g, b) > 0.05:
                return True
    return False


def mapped(literal, prop, body="", selector=""):
    r, g, b, a = parse(literal)
    light = lum(r, g, b)
    sat = saturation(r, g, b)
    if prop in SHADOW_PROPS:
        # Shadows and glows: dark ones soften, coloured ones stay.
        if light < 0.05:
            return f"rgba(15, 23, 42, {min(a, 1) * 0.35:.2f})"
        return None
    if a < 1:
        if light > 0.6 and sat < 0.12:
            return f"rgba(15, 23, 42, {min(0.12, a * 0.9):.3f})"
        if light < 0.05 and sat < 0.15:
            # A dark scrim over content stays a (lighter) scrim; a nearly
            # opaque dark bar is a surface and becomes the light one.
            if "backdrop" in selector or a < 0.6 or prop in TEXT_PROPS or BORDER_PROPS.match(prop):
                return f"rgba(15, 23, 42, {a * 0.45:.2f})"
            return "var(--bg-soft)" if light < 0.009 else "var(--panel)"
        return None
    if prop in TEXT_PROPS:
        if on_colour(body):
            return None
        if sat < 0.12:
            if light > 0.55:
                return "var(--ink)"
            if light > 0.18:
                return "var(--ink-dim)"
            return None  # dark text on a coloured fill stays dark
        if contrast((r, g, b), LIGHT_BG) < 4.5:
            return darken_to_read(r, g, b)
        return None
    if BORDER_PROPS.match(prop):
        if sat < 0.15 and light < 0.2:
            return "var(--line-2)" if light > 0.03 else "var(--line)"
        return None
    # Backgrounds and fills.
    if sat < 0.15 and light < 0.12:
        if light < 0.006:
            return "var(--bg)"
        if light < 0.009:
            return "var(--bg-soft)"
        if light < 0.02:
            return "var(--panel)"
        return "var(--panel-2)"
    if sat >= 0.15 and light < 0.05:
        # A dark tinted panel (a warning or success wash): a pale wash instead.
        tint = tuple(round(255 - (255 - v) * 0.12) for v in (r, g, b))
        return "#%02x%02x%02x" % tint
    return None


def strip_comments(text):
    return re.sub(r"/\*.*?\*/", "", text, flags=re.S)


def blocks(text):
    """Yield (media, selector, body) for every rule, one level of @media deep."""
    i, n = 0, len(text)
    stack = []
    start = 0
    while i < n:
        ch = text[i]
        if ch == "{":
            head = text[start:i].strip()
            stack.append(head)
            start = i + 1
        elif ch == "}":
            if stack:
                head = stack.pop()
                body = text[start:i]
                if not head.startswith("@") and "{" not in body:
                    media = [h for h in stack if h.startswith("@media")]
                    if not any(h.startswith("@") and not h.startswith("@media") for h in stack):
                        yield (media[-1] if media else None, head, body)
            start = i + 1
        i += 1


def scope(selector):
    parts = []
    for sel in selector.split(","):
        sel = sel.strip()
        if not sel:
            continue
        if sel.startswith(":root"):
            parts.append(':root[data-theme="light"]' + sel[5:])
        elif sel.startswith("html"):
            parts.append('html[data-theme="light"]' + sel[4:])
        else:
            parts.append(':root[data-theme="light"] ' + sel)
    return ", ".join(parts)


def main():
    out = [
        "/* Generated by frontend/scripts/light_theme.py — do not edit by hand.",
        "   Light-theme replacements for colours the stylesheets write as literals. */",
    ]
    for path in SOURCES:
        text = strip_comments(path.read_text())
        rules = []
        for media, selector, body in blocks(text):
            if "@keyframes" in selector or selector.startswith("from") or selector.startswith("to") or re.fullmatch(r"[\d.%, ]+", selector):
                continue
            changes = []
            for decl in body.split(";"):
                if ":" not in decl:
                    continue
                prop, value = decl.split(":", 1)
                prop = prop.strip().lower()
                if prop.startswith("--") or not COLOR.search(value):
                    continue
                important = "!important" in value
                # A literal that is only a var() fallback never shows: the
                # token is always defined.
                visible = FALLBACK.sub(lambda m: "_" * len(m.group(0)), value)
                pieces, last, changed = [], 0, False
                for match in COLOR.finditer(visible):
                    literal = value[match.start():match.end()]
                    try:
                        replacement = mapped(literal, "border" if prop == "border" else prop, body, selector)
                    except (ValueError, IndexError):
                        replacement = None
                    if replacement:
                        pieces.append(value[last:match.start()] + replacement)
                        last = match.end()
                        changed = True
                new = "".join(pieces) + value[last:]
                if changed:
                    new = new.replace("!important", "").strip()
                    changes.append(f"{prop}: {new}{' !important' if important else ''}")
            if changes:
                rules.append((media, scope(selector), changes))
        if not rules:
            continue
        out.append(f"\n/* {path.relative_to(ROOT.parent)} */")
        for media, selector, changes in rules:
            rule = f"{selector} {{ {'; '.join(changes)}; }}"
            out.append(f"{media} {{ {rule} }}" if media else rule)
    OUT.write_text("\n".join(out) + "\n")
    print(f"wrote {OUT.relative_to(ROOT.parent)}: {sum(1 for line in out if '{' in line)} rules")


if __name__ == "__main__":
    main()
