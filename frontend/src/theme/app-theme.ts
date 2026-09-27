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
  // A plugin theme ("<plugin>/<theme>", docs/plugins.md), or "" for Lectern's own.
  preset: string;
}
export const DEFAULT_APPEARANCE: Appearance = { theme: "dark", accent: "", zoom: 1, language: "", preset: "" };

// A plugin theme's colours: token overrides per mode, and an accent used
// when the person has not chosen one of their own.
export interface ThemePreset {
  accent?: string;
  dark?: Record<string, string>;
  light?: Record<string, string>;
}
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
  | "accent" | "accent-soft" | "accent-fill" | "indigo" | "cyan" | "blue" | "amber" | "red" | "green"
  | "on-accent" | "on-fill" | "overlay" | "scrim" | "paper";
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
    "accent-fill": "#7860d2",
    indigo: "#818cf8",
    cyan: "#38bdf8",
    blue: "#60a5fa",
    amber: "#fbbf24",
    red: "#f87171",
    green: "#34d399",
    "on-accent": "#ffffff",
    "on-fill": "#0b0a1a",
    overlay: "#ffffff",
    scrim: "#000000",
    // The page a document is drawn on (PDF pages, HTML previews, plots):
    // white in both themes, as the document was made.
    paper: "#ffffff",
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
    "accent-fill": "#6d28d9",
    indigo: "#4338ca",
    cyan: "#0369a1",
    blue: "#1d4ed8",
    amber: "#92400e",
    red: "#b91c1c",
    green: "#047857",
    "on-accent": "#ffffff",
    "on-fill": "#ffffff",
    overlay: "#0f172a",
    scrim: "#0f172a",
    paper: "#ffffff",
  },
};

// Which tokens are text, and which surfaces text is drawn on. The contrast test
// walks exactly these pairs.
export const TEXT_TOKENS: TokenName[] = ["ink", "ink-dim", "ink-faint", "accent-soft", "indigo", "cyan", "blue", "amber", "red", "green"];
export const SURFACE_TOKENS: TokenName[] = ["bg", "bg-soft", "panel", "panel-2"];

export function resolveMode(mode: ThemeMode, prefersDark: boolean): "dark" | "light" {
  return mode === "system" ? (prefersDark ? "dark" : "light") : mode;
}

// The full token set for one mode and accent. A custom accent is lifted or
// darkened only as far as it must be to stay legible on every surface.
export function themeTokens(mode: "dark" | "light", accent = "", overrides?: Record<string, string>): Palette & { "accent-wash": string } {
  const base = { ...palettes[mode] };
  if (overrides) {
    // A plugin theme may set any known token to a valid colour; then every
    // text token is lifted just enough to stay readable on every surface,
    // whatever the theme did to them.
    for (const [name, value] of Object.entries(overrides))
      if (name in base && parseColor(value)) base[name as TokenName] = value;
    for (const text of TEXT_TOKENS) base[text] = SURFACE_TOKENS.reduce((color, surface) => ensureContrast(color, base[surface]), base[text]);
  }
  if (accent && parseColor(accent)) {
    base.accent = accent;
    base["accent-fill"] = ensureContrast(mode === "dark" ? mix(accent, "#000000", 0.1) : accent, "#ffffff", 4.5);
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
    preset: typeof row.preset === "string" ? row.preset.slice(0, 140) : "",
  };
}

// The terminal page names its surfaces differently; map the same palette onto
// its tokens so its chrome follows the app theme too.
const terminalNames: Record<string, TokenName> = { surface: "panel", muted: "ink-dim" };

export function applyAppearance(
  appearance: Appearance,
  root: HTMLElement = document.documentElement,
  options: { terminal?: boolean; prefersDark?: boolean; preset?: ThemePreset } = {},
) {
  const prefersDark = options.prefersDark ?? (typeof matchMedia === "function" ? matchMedia("(prefers-color-scheme: dark)").matches : true);
  const mode = resolveMode(appearance.theme, prefersDark);
  const preset = options.preset;
  const tokens = themeTokens(mode, appearance.accent || preset?.accent || "", preset?.[mode]);
  root.dataset.theme = mode;
  root.classList.toggle("dark", mode === "dark");
  root.style.colorScheme = mode;
  // Both documents use the same tokens; the terminal page also has two
  // older names for its surfaces.
  for (const [name, value] of Object.entries(tokens)) root.style.setProperty("--" + name, value);
  if (options.terminal) for (const [name, token] of Object.entries(terminalNames)) root.style.setProperty("--" + name, tokens[token]);
  root.style.setProperty("--ui-zoom", String(appearance.zoom));
  const meta = document.querySelector('meta[name="theme-color"]');
  meta?.setAttribute("content", tokens.bg);
  return mode;
}
