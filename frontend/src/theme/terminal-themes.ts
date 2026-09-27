// Terminal colour schemes. The first three keep the ids the terminal stored
// before this list existed (slate, black, light), so a saved choice survives.
// The rest are widely published palettes; values follow each project's own
// published scheme.
import type { ITheme } from "@xterm/xterm";
import { contrast, parseColor } from "./color";

export interface TerminalTheme {
  id: string;
  name: string;
  light: boolean;
  theme: ITheme;
  custom?: boolean;
}

const ansi = (list: string[]): Partial<ITheme> => {
  const names = ["black", "red", "green", "yellow", "blue", "magenta", "cyan", "white", "brightBlack", "brightRed", "brightGreen", "brightYellow", "brightBlue", "brightMagenta", "brightCyan", "brightWhite"] as const;
  const out: Partial<ITheme> = {};
  list.forEach((color, index) => {
    const name = names[index];
    if (name) out[name] = color;
  });
  return out;
};

const scheme = (id: string, name: string, light: boolean, background: string, foreground: string, cursor: string, selection: string, colors: string[]): TerminalTheme => ({
  id,
  name,
  light,
  theme: { background, foreground, cursor, cursorAccent: background, selectionBackground: selection, ...ansi(colors) },
});

export const builtinTerminalThemes: TerminalTheme[] = [
  scheme("slate", "Lectern Slate", false, "#10121c", "#e0e4f0", "#b6a4ff", "#66578a",
    ["#1d2030", "#f07178", "#a6da95", "#eed49f", "#8aadf4", "#c6a0f6", "#8bd5ca", "#cad3f5", "#5b6078", "#f38ba8", "#b8e3a8", "#f5e0b0", "#a6c1f7", "#d7b8fa", "#a5e3da", "#ffffff"]),
  scheme("black", "Black", false, "#000000", "#e9e9e9", "#ffffff", "#444444",
    ["#000000", "#cd3131", "#0dbc79", "#e5e510", "#2472c8", "#bc3fbc", "#11a8cd", "#e5e5e5", "#666666", "#f14c4c", "#23d18b", "#f5f543", "#3b8eea", "#d670d6", "#29b8db", "#ffffff"]),
  scheme("light", "Paper", true, "#f6f4ee", "#202334", "#453575", "#c7b9ee",
    ["#202334", "#b3261e", "#1e7a3c", "#8a6100", "#2455a4", "#7b3aa0", "#0f6f7a", "#d7d3c8", "#5c5f6e", "#c9372c", "#228b45", "#9c6f00", "#2d64bd", "#8c47b3", "#12808c", "#f6f4ee"]),
  scheme("solarized-dark", "Solarized Dark", false, "#002b36", "#93a1a1", "#93a1a1", "#073642",
    ["#073642", "#dc322f", "#859900", "#b58900", "#268bd2", "#d33682", "#2aa198", "#eee8d5", "#586e75", "#cb4b16", "#859900", "#b58900", "#268bd2", "#6c71c4", "#2aa198", "#fdf6e3"]),
  scheme("solarized-light", "Solarized Light", true, "#fdf6e3", "#586e75", "#586e75", "#eee8d5",
    ["#073642", "#dc322f", "#859900", "#b58900", "#268bd2", "#d33682", "#2aa198", "#eee8d5", "#002b36", "#cb4b16", "#586e75", "#657b83", "#839496", "#6c71c4", "#93a1a1", "#fdf6e3"]),
  scheme("dracula", "Dracula", false, "#282a36", "#f8f8f2", "#f8f8f2", "#44475a",
    ["#21222c", "#ff5555", "#50fa7b", "#f1fa8c", "#bd93f9", "#ff79c6", "#8be9fd", "#f8f8f2", "#6272a4", "#ff6e6e", "#69ff94", "#ffffa5", "#d6acff", "#ff92df", "#a4ffff", "#ffffff"]),
  scheme("nord", "Nord", false, "#2e3440", "#d8dee9", "#d8dee9", "#434c5e",
    ["#3b4252", "#bf616a", "#a3be8c", "#ebcb8b", "#81a1c1", "#b48ead", "#88c0d0", "#e5e9f0", "#4c566a", "#bf616a", "#a3be8c", "#ebcb8b", "#81a1c1", "#b48ead", "#8fbcbb", "#eceff4"]),
  scheme("gruvbox-dark", "Gruvbox Dark", false, "#282828", "#ebdbb2", "#ebdbb2", "#504945",
    ["#282828", "#cc241d", "#98971a", "#d79921", "#458588", "#b16286", "#689d6a", "#a89984", "#928374", "#fb4934", "#b8bb26", "#fabd2f", "#83a598", "#d3869b", "#8ec07c", "#ebdbb2"]),
  scheme("gruvbox-light", "Gruvbox Light", true, "#fbf1c7", "#3c3836", "#3c3836", "#d5c4a1",
    ["#fbf1c7", "#cc241d", "#98971a", "#d79921", "#458588", "#b16286", "#689d6a", "#7c6f64", "#928374", "#9d0006", "#79740e", "#b57614", "#076678", "#8f3f71", "#427b58", "#3c3836"]),
  scheme("one-dark", "One Dark", false, "#282c34", "#abb2bf", "#528bff", "#3e4451",
    ["#282c34", "#e06c75", "#98c379", "#e5c07b", "#61afef", "#c678dd", "#56b6c2", "#abb2bf", "#5c6370", "#e06c75", "#98c379", "#e5c07b", "#61afef", "#c678dd", "#56b6c2", "#ffffff"]),
  scheme("one-light", "One Light", true, "#fafafa", "#383a42", "#526fff", "#e5e5e6",
    ["#383a42", "#e45649", "#50a14f", "#c18401", "#4078f2", "#a626a4", "#0184bc", "#a0a1a7", "#696c77", "#e45649", "#50a14f", "#c18401", "#4078f2", "#a626a4", "#0184bc", "#fafafa"]),
  scheme("tokyo-night", "Tokyo Night", false, "#1a1b26", "#c0caf5", "#c0caf5", "#33467c",
    ["#15161e", "#f7768e", "#9ece6a", "#e0af68", "#7aa2f7", "#bb9af7", "#7dcfff", "#a9b1d6", "#414868", "#f7768e", "#9ece6a", "#e0af68", "#7aa2f7", "#bb9af7", "#7dcfff", "#c0caf5"]),
  scheme("tokyo-night-day", "Tokyo Night Day", true, "#e1e2e7", "#3760bf", "#3760bf", "#b6bfe2",
    ["#e9e9ed", "#f52a65", "#587539", "#8c6c3e", "#2e7de9", "#9854f1", "#007197", "#6172b0", "#a1a6c5", "#f52a65", "#587539", "#8c6c3e", "#2e7de9", "#9854f1", "#007197", "#3760bf"]),
  scheme("catppuccin-mocha", "Catppuccin Mocha", false, "#1e1e2e", "#cdd6f4", "#f5e0dc", "#585b70",
    ["#45475a", "#f38ba8", "#a6e3a1", "#f9e2af", "#89b4fa", "#f5c2e7", "#94e2d5", "#bac2de", "#585b70", "#f38ba8", "#a6e3a1", "#f9e2af", "#89b4fa", "#f5c2e7", "#94e2d5", "#a6adc8"]),
  scheme("catppuccin-latte", "Catppuccin Latte", true, "#eff1f5", "#4c4f69", "#dc8a78", "#acb0be",
    ["#5c5f77", "#d20f39", "#40a02b", "#df8e1d", "#1e66f5", "#ea76cb", "#179299", "#acb0be", "#6c6f85", "#d20f39", "#40a02b", "#df8e1d", "#1e66f5", "#ea76cb", "#179299", "#bcc0cc"]),
  scheme("monokai", "Monokai", false, "#272822", "#f8f8f2", "#f8f8f0", "#49483e",
    ["#272822", "#f92672", "#a6e22e", "#f4bf75", "#66d9ef", "#ae81ff", "#a1efe4", "#f8f8f2", "#75715e", "#f92672", "#a6e22e", "#f4bf75", "#66d9ef", "#ae81ff", "#a1efe4", "#f9f8f5"]),
  scheme("github-dark", "GitHub Dark", false, "#0d1117", "#c9d1d9", "#58a6ff", "#264f78",
    ["#484f58", "#ff7b72", "#3fb950", "#d29922", "#58a6ff", "#bc8cff", "#39c5cf", "#b1bac4", "#6e7681", "#ffa198", "#56d364", "#e3b341", "#79c0ff", "#d2a8ff", "#56d4dd", "#f0f6fc"]),
  scheme("github-light", "GitHub Light", true, "#ffffff", "#24292f", "#0969da", "#b6e3ff",
    ["#24292f", "#cf222e", "#116329", "#4d2d00", "#0969da", "#8250df", "#1b7c83", "#6e7781", "#57606a", "#a40e26", "#1a7f37", "#633c01", "#218bff", "#a475f9", "#3192aa", "#8c959f"]),
  scheme("ayu-dark", "Ayu Dark", false, "#0b0e14", "#bfbdb6", "#e6b450", "#273747",
    ["#1e232b", "#ea6c73", "#7fd962", "#f9af4f", "#53bdfa", "#cda1fa", "#90e1c6", "#c7c7c7", "#686868", "#f07178", "#aad94c", "#ffb454", "#59c2ff", "#d2a6ff", "#95e6cb", "#ffffff"]),
  scheme("ayu-light", "Ayu Light", true, "#fcfcfc", "#5c6166", "#ffaa33", "#d1e4f4",
    ["#000000", "#ea6c6d", "#6cbf43", "#eca944", "#3199e1", "#9e75c7", "#46ba94", "#bababa", "#686868", "#f07171", "#86b300", "#f2ae49", "#399ee6", "#a37acc", "#4cbf99", "#d1d1d1"]),
  scheme("night-owl", "Night Owl", false, "#011627", "#d6deeb", "#80a4c2", "#1d3b53",
    ["#011627", "#ef5350", "#22da6e", "#c5e478", "#82aaff", "#c792ea", "#21c7a8", "#ffffff", "#575656", "#ef5350", "#22da6e", "#ffeb95", "#82aaff", "#c792ea", "#7fdbca", "#ffffff"]),
  scheme("everforest-dark", "Everforest Dark", false, "#2d353b", "#d3c6aa", "#d3c6aa", "#475258",
    ["#475258", "#e67e80", "#a7c080", "#dbbc7f", "#7fbbb3", "#d699b6", "#83c092", "#d3c6aa", "#475258", "#e67e80", "#a7c080", "#dbbc7f", "#7fbbb3", "#d699b6", "#83c092", "#d3c6aa"]),
  scheme("rose-pine", "Rosé Pine", false, "#191724", "#e0def4", "#524f67", "#403d52",
    ["#26233a", "#eb6f92", "#31748f", "#f6c177", "#9ccfd8", "#c4a7e7", "#ebbcba", "#e0def4", "#6e6a86", "#eb6f92", "#31748f", "#f6c177", "#9ccfd8", "#c4a7e7", "#ebbcba", "#e0def4"]),
  scheme("rose-pine-dawn", "Rosé Pine Dawn", true, "#faf4ed", "#575279", "#9893a5", "#dfdad9",
    ["#f2e9e1", "#b4637a", "#286983", "#ea9d34", "#56949f", "#907aa9", "#d7827e", "#575279", "#9893a5", "#b4637a", "#286983", "#ea9d34", "#56949f", "#907aa9", "#d7827e", "#575279"]),
  scheme("tomorrow-night", "Tomorrow Night", false, "#1d1f21", "#c5c8c6", "#c5c8c6", "#373b41",
    ["#1d1f21", "#cc6666", "#b5bd68", "#f0c674", "#81a2be", "#b294bb", "#8abeb7", "#c5c8c6", "#969896", "#cc6666", "#b5bd68", "#f0c674", "#81a2be", "#b294bb", "#8abeb7", "#ffffff"]),
  scheme("tomorrow", "Tomorrow", true, "#ffffff", "#4d4d4c", "#4d4d4c", "#d6d6d6",
    ["#000000", "#c82829", "#718c00", "#eab700", "#4271ae", "#8959a8", "#3e999f", "#ffffff", "#8e908c", "#c82829", "#718c00", "#eab700", "#4271ae", "#8959a8", "#3e999f", "#ffffff"]),
  scheme("kanagawa", "Kanagawa", false, "#1f1f28", "#dcd7ba", "#c8c093", "#2d4f67",
    ["#090618", "#c34043", "#76946a", "#c0a36e", "#7e9cd8", "#957fb8", "#6a9589", "#c8c093", "#727169", "#e82424", "#98bb6c", "#e6c384", "#7fb4ca", "#938aa9", "#7aa89f", "#dcd7ba"]),
];

export function isTerminalTheme(value: unknown): value is TerminalTheme {
  if (!value || typeof value !== "object") return false;
  const row = value as TerminalTheme;
  return typeof row.id === "string" && typeof row.name === "string" && !!row.theme && typeof row.theme === "object" &&
    typeof row.theme.background === "string" && typeof row.theme.foreground === "string";
}

export function allTerminalThemes(custom: unknown): TerminalTheme[] {
  const extra = Array.isArray(custom) ? custom.filter(isTerminalTheme).map((row) => ({ ...row, custom: true })) : [];
  return [...builtinTerminalThemes, ...extra.filter((row) => !builtinTerminalThemes.some((b) => b.id === row.id))];
}

export function findTerminalTheme(id: string, custom: unknown): TerminalTheme {
  return allTerminalThemes(custom).find((row) => row.id === id) || builtinTerminalThemes[0]!;
}

// ---- import from common theme formats ----------------------------------------
// Windows Terminal scheme JSON, iTerm2 .itermcolors (XML plist), and the
// key/value files of kitty, Ghostty, Alacritty (TOML), foot and Xresources.
// Each yields the same ITheme; unrecognised input is an error, never a guess.

const wtNames: Record<string, keyof ITheme> = {
  background: "background", foreground: "foreground", cursorColor: "cursor", selectionBackground: "selectionBackground",
  black: "black", red: "red", green: "green", yellow: "yellow", blue: "blue", purple: "magenta", magenta: "magenta", cyan: "cyan", white: "white",
  brightBlack: "brightBlack", brightRed: "brightRed", brightGreen: "brightGreen", brightYellow: "brightYellow", brightBlue: "brightBlue",
  brightPurple: "brightMagenta", brightMagenta: "brightMagenta", brightCyan: "brightCyan", brightWhite: "brightWhite",
};
const indexNames = ["black", "red", "green", "yellow", "blue", "magenta", "cyan", "white", "brightBlack", "brightRed", "brightGreen", "brightYellow", "brightBlue", "brightMagenta", "brightCyan", "brightWhite"] as const;

function hex(value: string): string | null {
  const rgb = parseColor(value.replace(/^0x/i, "#").replace(/^['"]|['"]$/g, ""));
  return rgb ? "#" + rgb.map((n) => n.toString(16).padStart(2, "0")).join("") : null;
}

function fromWindowsTerminal(text: string): { name?: string; theme: ITheme } | null {
  let data: unknown;
  try {
    data = JSON.parse(text);
  } catch {
    return null;
  }
  const row = Array.isArray(data) ? data[0] : data && typeof data === "object" && "schemes" in data && Array.isArray((data as { schemes: unknown[] }).schemes) ? (data as { schemes: unknown[] }).schemes[0] : data;
  if (!row || typeof row !== "object") return null;
  const theme: ITheme = {};
  for (const [key, value] of Object.entries(row as Record<string, unknown>)) {
    const name = wtNames[key];
    const color = typeof value === "string" ? hex(value) : null;
    if (name && color) (theme as Record<string, string>)[name] = color;
  }
  return { name: typeof (row as { name?: unknown }).name === "string" ? (row as { name: string }).name : undefined, theme };
}

function fromITerm(text: string): { theme: ITheme } | null {
  if (!/<plist/i.test(text)) return null;
  const theme: ITheme = {};
  const entry = /<key>([^<]+)<\/key>\s*<dict>([\s\S]*?)<\/dict>/g;
  const component = (body: string, name: string) => {
    const match = new RegExp(`<key>${name} Component</key>\\s*<real>([^<]+)</real>`).exec(body);
    return match ? Math.round(Math.max(0, Math.min(1, Number(match[1]))) * 255) : null;
  };
  for (const match of text.matchAll(entry)) {
    const key = match[1]!.trim(),
      body = match[2]!;
    const r = component(body, "Red"),
      g = component(body, "Green"),
      b = component(body, "Blue");
    if (r === null || g === null || b === null) continue;
    const color = "#" + [r, g, b].map((n) => n.toString(16).padStart(2, "0")).join("");
    const ansiMatch = /^Ansi (\d+) Color$/.exec(key);
    if (ansiMatch && Number(ansiMatch[1]) < 16) (theme as Record<string, string>)[indexNames[Number(ansiMatch[1])]!] = color;
    else if (key === "Background Color") theme.background = color;
    else if (key === "Foreground Color") theme.foreground = color;
    else if (key === "Cursor Color") theme.cursor = color;
    else if (key === "Selection Color") theme.selectionBackground = color;
  }
  return { theme };
}

// kitty (`color0 #000`, `background #fff`), Ghostty (`palette = 0=#000`,
// `background = fff`), Alacritty TOML (`[colors.normal]` then `black = "#000"`),
// foot (`regular0=000000`) and Xresources (`*.color0: #000`).
function fromKeyValue(text: string): { theme: ITheme } | null {
  const theme: ITheme = {};
  let section = "";
  const set = (name: keyof ITheme | undefined, value: string) => {
    const color = hex(value.trim());
    if (name && color) (theme as Record<string, string>)[name] = color;
  };
  const colorNames = ["black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"];
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || /^[#!;]/.test(line)) continue;
    const header = /^\[([^\]]+)\]$/.exec(line);
    if (header) {
      section = header[1]!.trim();
      continue;
    }
    const kv = /^\*?\.?([\w.-]+)\s*[:=]?\s*(.+)$/.exec(line);
    if (!kv) continue;
    let key = kv[1]!.toLowerCase();
    let value = kv[2]!.trim().replace(/^=\s*/, "");
    if (key === "palette") {
      const pair = /^(\d+)\s*=\s*(.+)$/.exec(value);
      if (pair && Number(pair[1]) < 16) set(indexNames[Number(pair[1])], pair[2]!);
      continue;
    }
    const color = /^(?:color|regular|bright)(\d+)$/.exec(key);
    if (color) {
      const index = Number(color[1]) + (key.startsWith("bright") ? 8 : 0);
      if (index < 16) set(indexNames[index], value);
      continue;
    }
    if (section.endsWith("normal") || section.endsWith("bright")) {
      const index = colorNames.indexOf(key);
      if (index >= 0) set(indexNames[index + (section.endsWith("bright") ? 8 : 0)], value);
      continue;
    }
    if (section.endsWith("cursor") && key === "cursor") key = "cursor-color";
    const map: Record<string, keyof ITheme> = {
      background: "background", foreground: "foreground", cursor: "cursor", "cursor-color": "cursor", cursorcolor: "cursor",
      selection_background: "selectionBackground", "selection-background": "selectionBackground", selectionbackground: "selectionBackground",
    };
    value = value.replace(/^['"]|['"]$/g, "");
    set(map[key], value);
  }
  return { theme };
}

export function importTerminalTheme(text: string, fallbackName = "Imported"): TerminalTheme {
  const parsed = fromITerm(text) || fromWindowsTerminal(text) || fromKeyValue(text);
  const theme = parsed?.theme;
  if (!theme?.background || !theme.foreground)
    throw new Error("No background and foreground colours found. Supported: Windows Terminal JSON, iTerm2 .itermcolors, kitty, Ghostty, Alacritty, foot, Xresources.");
  const count = indexNames.filter((name) => theme[name]).length;
  if (count < 8) throw new Error(`Only ${count} of the 16 ANSI colours were found; a theme needs at least the first 8.`);
  const name = ((parsed && "name" in parsed && typeof parsed.name === "string" && parsed.name) || fallbackName).slice(0, 60);
  theme.cursor ??= theme.foreground;
  theme.cursorAccent ??= theme.background;
  const light = contrast(theme.background, "#000000") > contrast(theme.background, "#ffffff");
  return { id: "custom-" + name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "").slice(0, 40), name, light, theme, custom: true };
}
