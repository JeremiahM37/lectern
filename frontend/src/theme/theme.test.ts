import assert from "node:assert/strict";
import { test } from "node:test";
import { contrast, ensureContrast } from "./color";
import { ACCENT_PRESETS, DEFAULT_APPEARANCE, normalizeAppearance, palettes, SURFACE_TOKENS, TEXT_TOKENS, themeTokens } from "./app-theme";
import { builtinTerminalThemes, importTerminalTheme } from "./terminal-themes";

// WCAG 2.x AA: 4.5:1 for body text. Every text token must read on every
// surface in both modes, including the accent derived from any preset.
test("every text token meets WCAG AA on every surface, dark and light", () => {
  const failures: string[] = [];
  for (const mode of ["dark", "light"] as const)
    for (const accent of ACCENT_PRESETS.map((preset) => preset.value)) {
      const tokens = themeTokens(mode, accent);
      for (const text of TEXT_TOKENS)
        for (const surface of SURFACE_TOKENS) {
          const ratio = contrast(tokens[text], tokens[surface]);
          if (ratio < 4.5) failures.push(`${mode} accent=${accent || "default"} ${text} on ${surface}: ${ratio.toFixed(2)}`);
        }
    }
  assert.deepEqual(failures, []);
});

test("a custom accent is made legible rather than refused", () => {
  // Pale yellow on white and near-black on the dark theme both start illegible.
  for (const [mode, accent] of [["light", "#fff6a0"], ["dark", "#101010"]] as const) {
    const tokens = themeTokens(mode, accent);
    for (const surface of SURFACE_TOKENS)
      assert.ok(contrast(tokens["accent-soft"], tokens[surface]) >= 4.5, `${mode} ${accent} on ${surface}`);
  }
  assert.equal(ensureContrast("#000000", "#ffffff"), "#000000");
});

test("the two palettes define the same tokens", () => {
  assert.deepEqual(Object.keys(palettes.dark).sort(), Object.keys(palettes.light).sort());
});

test("stored appearance is validated, not trusted", () => {
  assert.deepEqual(normalizeAppearance({ theme: "neon", accent: "javascript:1", zoom: 9, language: 4, preset: 7 }), {
    theme: "system", accent: "", zoom: 1, language: "", preset: "",
  });
  assert.equal(normalizeAppearance(undefined).theme, "system");
  assert.equal(DEFAULT_APPEARANCE.theme, "system");
  assert.deepEqual(normalizeAppearance({ theme: "light", accent: "#123abc", zoom: 1.25, language: "fr", preset: "acme.x/forest" }), {
    theme: "light", accent: "#123abc", zoom: 1.25, language: "fr", preset: "acme.x/forest",
  });
});

test("a plugin theme's colours apply, and text stays readable on its surfaces", () => {
  for (const mode of ["dark", "light"] as const) {
    // A theme that makes a surface nearly the colour of the text on it.
    const surface = mode === "dark" ? "#6b7280" : "#9ca3af";
    const tokens = themeTokens(mode, "", { panel: surface, bg: surface, "no-such-token": "#000000", "bg-soft": "not a colour" });
    assert.equal(tokens.panel, surface);
    assert.equal(tokens["bg-soft"], palettes[mode]["bg-soft"]);
    assert.ok(!("no-such-token" in tokens));
    for (const text of TEXT_TOKENS)
      for (const s of SURFACE_TOKENS) assert.ok(contrast(tokens[text], tokens[s]) >= 4.5 - 1e-9, `${mode} ${text} on ${s}: ${contrast(tokens[text], tokens[s])}`);
  }
});

test("at least twenty terminal themes ship, light ones among them, all readable", () => {
  assert.ok(builtinTerminalThemes.length >= 20, String(builtinTerminalThemes.length));
  assert.ok(builtinTerminalThemes.filter((row) => row.light).length >= 6);
  assert.equal(new Set(builtinTerminalThemes.map((row) => row.id)).size, builtinTerminalThemes.length);
  for (const row of builtinTerminalThemes) {
    assert.ok(contrast(row.theme.foreground!, row.theme.background!) >= 4.5, `${row.name} foreground`);
    for (const name of ["black", "red", "green", "yellow", "blue", "magenta", "cyan", "white", "brightBlack", "brightWhite"] as const)
      assert.ok(row.theme[name], `${row.name} ${name}`);
  }
  for (const id of ["slate", "black", "light"]) assert.ok(builtinTerminalThemes.some((row) => row.id === id), id);
});

const colors16 = ["#000000", "#cd3131", "#0dbc79", "#e5e510", "#2472c8", "#bc3fbc", "#11a8cd", "#e5e5e5", "#666666", "#f14c4c", "#23d18b", "#f5f543", "#3b8eea", "#d670d6", "#29b8db", "#ffffff"];

test("imports a Windows Terminal scheme", () => {
  const names = ["black", "red", "green", "yellow", "blue", "purple", "cyan", "white", "brightBlack", "brightRed", "brightGreen", "brightYellow", "brightBlue", "brightPurple", "brightCyan", "brightWhite"];
  const scheme: Record<string, string> = { name: "My Scheme", background: "#101010", foreground: "#EEEEEE", cursorColor: "#ffcc00" };
  names.forEach((name, i) => (scheme[name] = colors16[i]!));
  const theme = importTerminalTheme(JSON.stringify(scheme));
  assert.equal(theme.name, "My Scheme");
  assert.equal(theme.id, "custom-my-scheme");
  assert.equal(theme.theme.background, "#101010");
  assert.equal(theme.theme.foreground, "#eeeeee");
  assert.equal(theme.theme.magenta, "#bc3fbc");
  assert.equal(theme.theme.cursor, "#ffcc00");
  assert.equal(theme.light, false);
});

test("imports an iTerm2 .itermcolors plist", () => {
  const entry = (key: string, hex: string) => {
    const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255);
    return `<key>${key}</key><dict><key>Blue Component</key><real>${b}</real><key>Green Component</key><real>${g}</real><key>Red Component</key><real>${r}</real></dict>`;
  };
  const body = colors16.map((c, i) => entry(`Ansi ${i} Color`, c)).join("") + entry("Background Color", "#fafafa") + entry("Foreground Color", "#222222");
  const theme = importTerminalTheme(`<?xml version="1.0"?><plist version="1.0"><dict>${body}</dict></plist>`, "Solar");
  assert.equal(theme.theme.background, "#fafafa");
  assert.equal(theme.theme.brightBlue, "#3b8eea");
  assert.equal(theme.light, true);
});

test("imports kitty, Ghostty, Alacritty and Xresources files", () => {
  const kitty = ["# kitty", "background #1e1e1e", "foreground #d4d4d4", ...colors16.map((c, i) => `color${i} ${c}`)].join("\n");
  assert.equal(importTerminalTheme(kitty).theme.green, "#0dbc79");
  const ghostty = ["background = 1e1e1e", "foreground = d4d4d4", ...colors16.map((c, i) => `palette = ${i}=${c}`)].join("\n");
  assert.equal(importTerminalTheme(ghostty).theme.brightWhite, "#ffffff");
  const names = ["black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"];
  const alacritty = ["[colors.primary]", 'background = "0x1e1e1e"', 'foreground = "#d4d4d4"', "[colors.normal]", ...names.map((n, i) => `${n} = "${colors16[i]}"`), "[colors.bright]", ...names.map((n, i) => `${n} = "${colors16[i + 8]}"`)].join("\n");
  const imported = importTerminalTheme(alacritty);
  assert.equal(imported.theme.background, "#1e1e1e");
  assert.equal(imported.theme.brightRed, "#f14c4c");
  const xres = ["! comment", "*.background: #1e1e1e", "*.foreground: #d4d4d4", ...colors16.map((c, i) => `*.color${i}: ${c}`)].join("\n");
  assert.equal(importTerminalTheme(xres).theme.yellow, "#e5e510");
});

test("an unrecognisable file is an error, not a half theme", () => {
  assert.throws(() => importTerminalTheme("hello world"), /background and foreground/);
  assert.throws(() => importTerminalTheme("background #000\nforeground #fff\ncolor0 #000"), /ANSI/);
});

test("white text on the accent button fill, and badge text on status fills, read in both themes", () => {
  for (const mode of ["dark", "light"] as const)
    for (const accent of ACCENT_PRESETS.map((preset) => preset.value)) {
      const tokens = themeTokens(mode, accent);
      assert.ok(contrast(tokens["on-accent"], tokens["accent-fill"]) >= 4.5, `${mode} ${accent} on-accent/accent-fill`);
    }
  for (const mode of ["dark", "light"] as const) {
    const tokens = themeTokens(mode);
    for (const fill of ["amber", "green", "red", "cyan", "accent-soft"] as const)
      assert.ok(contrast(tokens["on-fill"], tokens[fill]) >= 4.5, `${mode} on-fill/${fill}: ${contrast(tokens["on-fill"], tokens[fill]).toFixed(2)}`);
  }
});
