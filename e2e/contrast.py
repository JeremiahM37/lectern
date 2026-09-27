"""In-page WCAG contrast audit shared by the light-mode sweep.

For every visible element that holds its own text, the text colour (with its
alpha) is composited over the effective background — the nearest ancestor
backgrounds, layered — and the ratio checked against WCAG AA: 4.5:1, or 3:1 for
large text. Disabled controls and placeholder text are exempt (WCAG 1.4.3).
Anything drawn over an image or gradient is skipped rather than guessed.
"""

AUDIT = r"""
(root) => {
  const parse = (value) => {
    const m = /rgba?\(([^)]+)\)/.exec(value || "");
    if (!m) {
      const srgb = /color\(srgb ([\d.]+) ([\d.]+) ([\d.]+)(?: \/ ([\d.]+))?\)/.exec(value || "");
      if (!srgb) return null;
      return [srgb[1] * 255, srgb[2] * 255, srgb[3] * 255, srgb[4] === undefined ? 1 : Number(srgb[4])];
    }
    const p = m[1].split(/[ ,/]+/).filter(Boolean).map(Number);
    return [p[0], p[1], p[2], p.length > 3 ? p[3] : 1];
  };
  const over = (top, bottom) => {
    const a = top[3];
    return [0, 1, 2].map((i) => top[i] * a + bottom[i] * (1 - a)).concat(1);
  };
  const lum = (c) => {
    const ch = (v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; };
    return 0.2126 * ch(c[0]) + 0.7152 * ch(c[1]) + 0.0722 * ch(c[2]);
  };
  const ratio = (a, b) => { const x = lum(a), y = lum(b); return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05); };
  const backdrop = (el) => {
    const layers = [];
    for (let node = el; node; node = node.parentElement) {
      const style = getComputedStyle(node);
      if (style.backgroundImage && style.backgroundImage !== "none") return null;
      const bg = parse(style.backgroundColor);
      if (bg && bg[3] > 0) {
        layers.push(bg);
        if (bg[3] >= 1) break;
      }
      if (node.tagName === "DIALOG" && node.open && getComputedStyle(node).position === "fixed") {}
    }
    let base = parse(getComputedStyle(document.documentElement).backgroundColor);
    if (!base || base[3] < 1) base = parse(getComputedStyle(document.body).backgroundColor) || [255, 255, 255, 1];
    if (base[3] < 1) base = over(base, [255, 255, 255, 1]);
    for (let i = layers.length - 1; i >= 0; i--) base = over(layers[i], base);
    return base;
  };
  const failures = [];
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const seen = new Set();
  for (let text = walker.nextNode(); text; text = walker.nextNode()) {
    if (!text.textContent.trim()) continue;
    const el = text.parentElement;
    if (!el || seen.has(el)) continue;
    seen.add(el);
    if (el.closest("[aria-hidden='true'],[hidden],[inert],svg,.xterm,.xterm-screen,.term-swatch,.terminal-host,.frozen,.swatch,kbd.command-shortcut")) continue;
    if (el.closest(":disabled,[aria-disabled='true']")) continue;
    const box = el.getBoundingClientRect();
    if (!box.width || !box.height || box.bottom < 0 || box.top > innerHeight || box.right < 0 || box.left > innerWidth) continue;
    const style = getComputedStyle(el);
    if (style.visibility !== "visible" || Number(style.opacity) < 0.1) continue;
    let hiddenByOpacity = false;
    for (let node = el; node; node = node.parentElement) if (Number(getComputedStyle(node).opacity) < 0.5) { hiddenByOpacity = true; break; }
    if (hiddenByOpacity) continue;
    const fg = parse(style.color);
    const bg = backdrop(el);
    if (!fg || !bg) continue;
    const r = ratio(over(fg, bg), bg);
    const size = parseFloat(style.fontSize), bold = Number(style.fontWeight) >= 700;
    const need = size >= 24 || (bold && size >= 18.66) ? 3 : 4.5;
    if (r + 0.01 < need) {
      const id = el.id ? "#" + el.id : "";
      const cls = typeof el.className === "string" && el.className ? "." + el.className.trim().split(/\s+/).join(".") : "";
      failures.push(`${el.tagName.toLowerCase()}${id}${cls} "${text.textContent.trim().slice(0, 40)}" ${r.toFixed(2)} < ${need} (${style.color} on rgb(${bg.slice(0, 3).map(Math.round).join(",")}))`);
    }
  }
  return failures;
}
"""


def audit(target, root_selector="body"):
    """Contrast failures on a page or frame, as readable strings."""
    return target.locator(root_selector).first.evaluate(AUDIT)
