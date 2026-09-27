// Terminal settings that belong to the person rather than the device: colour
// scheme, line spacing, program clipboard access and find defaults. Font size
// stays per device (a phone and a monitor want different type) in the
// terminal's own local preferences.
import { useSyncExternalStore } from "react";
import type { ITheme } from "@xterm/xterm";
import type { JsonValue } from "../api/client";
import { getPref, setPref, subscribePrefs } from "../prefs/store";
import { findTerminalTheme, type TerminalTheme } from "./terminal-themes";

export const TERMINAL_KEY = "terminal";
export const THEMES_KEY = "terminal.themes";

export interface TerminalPersonPrefs {
  // "" means "not chosen here": the device's own choice stands.
  theme: string;
  lineHeight: number;
  // Programs may copy to the clipboard with OSC 52 (tmux, vim, ssh sessions).
  osc52: boolean;
  findCase: boolean;
  findRegex: boolean;
  findWord: boolean;
}
export const DEFAULT_TERMINAL_PREFS: TerminalPersonPrefs = { theme: "", lineHeight: 0, osc52: true, findCase: false, findRegex: false, findWord: false };

export function normalizeTerminalPrefs(value: unknown): TerminalPersonPrefs {
  const row = value && typeof value === "object" ? (value as Partial<TerminalPersonPrefs>) : {};
  return {
    theme: typeof row.theme === "string" ? row.theme.slice(0, 60) : "",
    lineHeight: [1, 1.15, 1.3].includes(Number(row.lineHeight)) ? Number(row.lineHeight) : 0,
    osc52: row.osc52 !== false,
    findCase: row.findCase === true,
    findRegex: row.findRegex === true,
    findWord: row.findWord === true,
  };
}

let raw: unknown = undefined;
let cached = DEFAULT_TERMINAL_PREFS;
export function readTerminalPrefs(): TerminalPersonPrefs {
  const next = getPref<unknown>(TERMINAL_KEY, DEFAULT_TERMINAL_PREFS);
  if (next !== raw) {
    raw = next;
    cached = normalizeTerminalPrefs(next);
  }
  return cached;
}

export function saveTerminalPrefs(change: Partial<TerminalPersonPrefs>) {
  setPref(TERMINAL_KEY, { ...readTerminalPrefs(), ...change } as unknown as JsonValue);
}

export function useTerminalPrefs() {
  return useSyncExternalStore(subscribePrefs, readTerminalPrefs, readTerminalPrefs);
}

export function customThemes(): unknown {
  return getPref<unknown>(THEMES_KEY, []);
}

export function saveCustomThemes(list: TerminalTheme[]) {
  setPref(THEMES_KEY, list.slice(0, 30) as unknown as JsonValue);
}

export function resolveTerminalTheme(id: string): ITheme {
  return findTerminalTheme(id, customThemes()).theme;
}
