// App-wide appearance: System/Dark/Light, an accent colour and UI zoom, all
// applied as CSS custom properties on <html>. The stylesheets read only these
// tokens, so switching theme is a property swap, not a second stylesheet. The
// values here are the source of truth; theme.test.ts holds every text token to
// WCAG AA against every surface it is drawn on.
import { alpha, ensureContrast, mix, parseColor } from "./color";

export type ThemeMode = "system" | "dark" | "light";
export interface Appearance {
  theme: ThemeMode;
  // An accent colour as #rrggbb, or "" for the house violet.
  accent: string;
  // UI zoom as a factor (0.8–1.5).
  zoom: number;
  // BCP 47 language tag, or "" to follow the browser.
  language: string;
}
export const DEFAULT_APPEARANCE: Appearance = { theme: "dark", accent: "", zoom: 1, language: "" };
export const ZOOM_STEPS = [0.8, 0.9, 1, 1.1, 1.25, 1.5];
export const ACCENT_PRESETS: { name: string; value: string }[] = [
  { name: "Violet", value: "" },
  { name: "Indigo", value: "#6366f1" },
  { name: "Blue", value: "#3b82f6" },
  { name: "Teal", value: "#14b8a6" },
  { name: "Green", value: "#22c55e" },
  { name: "Amber", value: "#f59e0b" },
  { name: "Orange", value: "#f97316" },
  { name: "Rose", value: "#f43f5e" },
  { name: "Pink", value: "#ec4899" },
];

export type TokenName =
  | "bg" | "bg-soft" | "panel" | "panel-2" | "line" | "line-2"
  | "ink" | "ink-dim" | "ink-faint"
  | "accent" | "accent-soft" | "indigo" | "cyan" | "amber" | "red" | "green";
export type Palette = Record<TokenName, string>;

export const palettes: Record<"dark" | "light", Palette> = {
  dark: {
    bg: "#0c1018",
    "bg-soft": "#101620",
    panel: "#151b26",
    "panel-2": "#1d2533",
    line: "#27303e",
    "line-2": "#384457",
    ink: "#e2e8f0",
    "ink-dim": "#97a2bd",
    "ink-faint": "#8793a7",
    accent: "#8b5cf6",
    "accent-soft": "#a78bfa",
    indigo: "#818cf8",
    cyan: "#38bdf8",
    amber: "#fbbf24",
    red: "#f87171",
    green: "#34d399",
  },
  light: {
    bg: "#f4f5f9",
    "bg-soft": "#eceef4",
    panel: "#ffffff",
    "panel-2": "#f0f2f7",
    line: "#d8dce6",
    "line-2": "#bcc3d2",
    ink: "#141824",
    "ink-dim": "#434b5f",
    "ink-faint": "#565f73",
    accent: "#7c3aed",
    "accent-soft": "#6d28d9",
    indigo: "#4338ca",
    cyan: "#0369a1",
    amber: "#92400e",
    red: "#b91c1c",
    green: "#047857",
  },
};

// Which tokens are text, and which surfaces text is drawn on. The contrast test
// walks exactly these pairs.
export const TEXT_TOKENS: TokenName[] = ["ink", "ink-dim", "ink-faint", "accent-soft", "indigo", "cyan", "amber", "red", "green"];
export const SURFACE_TOKENS: TokenName[] = ["bg", "bg-soft", "panel", "panel-2"];

export function resolveMode(mode: ThemeMode, prefersDark: boolean): "dark" | "light" {
  return mode === "system" ? (prefersDark ? "dark" : "light") : mode;
}

// The full token set for one mode and accent. A custom accent is lifted or
// darkened only as far as it must be to stay legible on every surface.
export function themeTokens(mode: "dark" | "light", accent = ""): Palette & { "accent-wash": string } {
  const base = { ...palettes[mode] };
  if (accent && parseColor(accent)) {
    base.accent = accent;
    const soft = mode === "dark" ? mix(accent, "#ffffff", 0.25) : mix(accent, "#000000", 0.15);
    base["accent-soft"] = SURFACE_TOKENS.reduce((color, surface) => ensureContrast(color, base[surface]), soft);
  }
  return { ...base, "accent-wash": alpha(base.accent, mode === "dark" ? 0.14 : 0.12) };
}

export function clampZoom(value: unknown): number {
  const n = Number(value);
  return Number.isFinite(n) && n >= 0.8 && n <= 1.5 ? n : 1;
}

export function normalizeAppearance(value: unknown): Appearance {
  const row = value && typeof value === "object" ? (value as Partial<Appearance>) : {};
  return {
    // Dark stays the default: an existing install looks the same until
    // someone chooses System or Light.
    theme: row.theme === "dark" || row.theme === "light" || row.theme === "system" ? row.theme : "dark",
    accent: typeof row.accent === "string" && parseColor(row.accent) ? row.accent : "",
    zoom: clampZoom(row.zoom),
    language: typeof row.language === "string" ? row.language.slice(0, 35) : "",
  };
}

// The terminal page names its surfaces differently; map the same palette onto
// its tokens so its chrome follows the app theme too.
const terminalNames: Record<string, TokenName> = { bg: "bg", surface: "panel", line: "line", ink: "ink", muted: "ink-dim", accent: "accent-soft" };

export function applyAppearance(
  appearance: Appearance,
  root: HTMLElement = document.documentElement,
  options: { terminal?: boolean; prefersDark?: boolean } = {},
) {
  const prefersDark = options.prefersDark ?? (typeof matchMedia === "function" ? matchMedia("(prefers-color-scheme: dark)").matches : true);
  const mode = resolveMode(appearance.theme, prefersDark);
  const tokens = themeTokens(mode, appearance.accent);
  root.dataset.theme = mode;
  root.classList.toggle("dark", mode === "dark");
  root.style.colorScheme = mode;
  // The terminal page gets the surface and ink tokens too (the generated
  // light overrides use them), plus its own names; its --accent keeps the
  // terminal's meaning (the readable accent).
  for (const [name, value] of Object.entries(tokens)) if (!options.terminal || !["accent", "accent-wash"].includes(name)) root.style.setProperty("--" + name, value);
  if (options.terminal) for (const [name, token] of Object.entries(terminalNames)) root.style.setProperty("--" + name, tokens[token]);
  root.style.setProperty("--ui-zoom", String(appearance.zoom));
  const meta = document.querySelector('meta[name="theme-color"]');
  meta?.setAttribute("content", tokens.bg);
  return mode;
}
