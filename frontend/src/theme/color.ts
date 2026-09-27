// Small colour helpers for theming: parsing, mixing, and the WCAG contrast
// ratio the theme tests hold every text token to.
export type RGB = [number, number, number];

export function parseColor(value: string): RGB | null {
  const hex = value.trim().replace(/^#/, "");
  if (/^[0-9a-f]{3}$/i.test(hex))
    return [0, 1, 2].map((i) => parseInt(hex[i]! + hex[i]!, 16)) as RGB;
  if (/^[0-9a-f]{6}([0-9a-f]{2})?$/i.test(hex))
    return [0, 2, 4].map((i) => parseInt(hex.slice(i, i + 2), 16)) as RGB;
  const rgb = /^rgba?\(\s*(\d+)[\s,]+(\d+)[\s,]+(\d+)/i.exec(value.trim());
  if (rgb) return [Number(rgb[1]), Number(rgb[2]), Number(rgb[3])];
  return null;
}

export function toHex([r, g, b]: RGB): string {
  return "#" + [r, g, b].map((n) => Math.round(Math.max(0, Math.min(255, n))).toString(16).padStart(2, "0")).join("");
}

export function mix(a: string, b: string, amount: number): string {
  const x = parseColor(a),
    y = parseColor(b);
  if (!x || !y) return a;
  return toHex([0, 1, 2].map((i) => x[i]! + (y[i]! - x[i]!) * amount) as RGB);
}

export function alpha(color: string, opacity: number): string {
  const rgb = parseColor(color);
  return rgb ? `rgba(${rgb[0]}, ${rgb[1]}, ${rgb[2]}, ${opacity})` : color;
}

function channel(value: number) {
  const s = value / 255;
  return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
}

export function luminance(color: string): number {
  const rgb = parseColor(color);
  if (!rgb) return 0;
  return 0.2126 * channel(rgb[0]) + 0.7152 * channel(rgb[1]) + 0.0722 * channel(rgb[2]);
}

export function contrast(a: string, b: string): number {
  const x = luminance(a),
    y = luminance(b);
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
}

// Moves a colour toward black or white, whichever the background is not, until
// it reads at `ratio` against that background. A chosen accent keeps its hue
// and only gives up as much lightness as legibility needs.
export function ensureContrast(color: string, background: string, ratio = 4.5): string {
  if (!parseColor(color)) return color;
  if (contrast(color, background) >= ratio) return toHex(parseColor(color)!);
  const target = luminance(background) > 0.35 ? "#000000" : "#ffffff";
  for (let step = 1; step <= 20; step++) {
    const next = mix(color, target, step / 20);
    if (contrast(next, background) >= ratio) return next;
  }
  return target;
}
