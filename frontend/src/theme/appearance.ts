// Keeps <html> in step with the person's appearance preference: on start
// (from the local copy, before any request answers), whenever it changes on
// this or another device, and when the system switches light/dark.
import { useSyncExternalStore } from "react";
import { getPref, setPref, subscribePrefs } from "../prefs/store";
import { setLocale } from "../i18n";
import { applyAppearance, DEFAULT_APPEARANCE, normalizeAppearance, type Appearance } from "./app-theme";

export const APPEARANCE_KEY = "appearance";

export function readAppearance(): Appearance {
  return normalizeAppearance(getPref<unknown>(APPEARANCE_KEY, DEFAULT_APPEARANCE));
}

let cached = readAppearance();
let cachedRaw: unknown = getPref<unknown>(APPEARANCE_KEY, DEFAULT_APPEARANCE);
export function currentAppearance() {
  const raw = getPref<unknown>(APPEARANCE_KEY, DEFAULT_APPEARANCE);
  if (raw !== cachedRaw) {
    cachedRaw = raw;
    cached = normalizeAppearance(raw);
  }
  return cached;
}

export function saveAppearance(change: Partial<Appearance>) {
  setPref(APPEARANCE_KEY, { ...currentAppearance(), ...change });
}

export function useAppearance(): Appearance {
  return useSyncExternalStore(subscribePrefs, currentAppearance, currentAppearance);
}

// Installs the live binding once per document. `terminal` maps the palette
// onto the terminal page's own token names and leaves zoom to the app.
export function bootAppearance(options: { terminal?: boolean } = {}) {
  const apply = () => {
    const appearance = currentAppearance();
    applyAppearance(options.terminal ? { ...appearance, zoom: 1 } : appearance, document.documentElement, { terminal: options.terminal });
    setLocale(appearance.language);
  };
  apply();
  subscribePrefs(apply);
  try {
    matchMedia("(prefers-color-scheme: dark)").addEventListener("change", apply);
  } catch {}
}

// CSS zoom on <body> scales lengths set inside it, but the page measures in
// unzoomed pixels. Divide a measured length by this before setting it back.
export function uiZoom(): number {
  try {
    const zoom = Number(getComputedStyle(document.body).zoom);
    return Number.isFinite(zoom) && zoom > 0 ? zoom : 1;
  } catch {
    return 1;
  }
}
