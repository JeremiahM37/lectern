// Translation: t("key", {vars}) looks a string up in the active catalog,
// falls back to English, then to the key's own default. English (en/) is the
// complete source catalog; every shipped language has a catalog with exactly
// the same keys (i18n.test.ts). A language's catalog is loaded when it is
// chosen, so the app downloads only the one it shows. "en-XA" is a
// pseudo-locale — every string accented and bracketed — that shows at a
// glance which text on screen does not go through t() yet.
import { useSyncExternalStore } from "react";
import en from "./en";

export type Catalog = Record<string, string>;
export interface Language {
  tag: string;
  name: string;
}

// Names are written in their own language, so a person who cannot read the
// current one can still find theirs.
export const LANGUAGES: Language[] = [
  { tag: "", name: "Browser default" },
  { tag: "en", name: "English" },
  { tag: "zh", name: "简体中文" },
  { tag: "ja", name: "日本語" },
  { tag: "ko", name: "한국어" },
  { tag: "es", name: "Español" },
  { tag: "fr", name: "Français" },
  { tag: "en-XA", name: "Pseudo-locale (translation check)" },
];
export const SHIPPED = ["en", "zh", "ja", "ko", "es", "fr"] as const;

const loaders: Record<string, () => Promise<{ default: Catalog }>> = {
  zh: () => import("./zh"),
  ja: () => import("./ja"),
  ko: () => import("./ko"),
  es: () => import("./es"),
  fr: () => import("./fr"),
};

const accents: Record<string, string> = {
  a: "á", b: "ƀ", c: "ç", d: "ð", e: "é", f: "ƒ", g: "ĝ", h: "ĥ", i: "í", j: "ĵ", k: "ķ", l: "ĺ", m: "ɱ", n: "ñ", o: "ó", p: "þ", q: "ǫ", r: "ŕ", s: "š", t: "ţ", u: "ú", v: "ṽ", w: "ŵ", x: "ẋ", y: "ý", z: "ž",
  A: "Á", B: "Ɓ", C: "Ç", D: "Ð", E: "É", F: "Ƒ", G: "Ĝ", H: "Ĥ", I: "Í", J: "Ĵ", K: "Ķ", L: "Ĺ", M: "Ṁ", N: "Ñ", O: "Ó", P: "Þ", Q: "Ǫ", R: "Ŕ", S: "Š", T: "Ţ", U: "Ú", V: "Ṽ", W: "Ŵ", X: "Ẋ", Y: "Ý", Z: "Ž",
};

// Placeholders ({name}) survive untouched so interpolation still works.
export function pseudo(text: string): string {
  const body = text.split(/(\{\w+\})/).map((part) => (/^\{\w+\}$/.test(part) ? part : part.replace(/[A-Za-z]/g, (ch) => accents[ch] || ch))).join("");
  return "[" + body + "]";
}

const catalogs: Record<string, Catalog> = { en };

let locale = "en";
let requestedTag = "";
let version = 0;
const listeners = new Set<() => void>();

// The language to show for a stored choice ("" = the browser's). Traditional
// Chinese is not shipped; rather than show Simplified to someone who reads
// Traditional, those tags fall back to English.
export function resolveLocale(requested: string, browser: readonly string[] = typeof navigator === "undefined" ? [] : navigator.languages || []): string {
  const candidates = requested ? [requested] : [...browser];
  for (const raw of candidates) {
    const tag = raw.replace(/_/g, "-");
    if (tag === "en-XA") return tag;
    const lower = tag.toLowerCase();
    if (/^zh-(tw|hk|mo|hant)/.test(lower)) return "en";
    const base = lower.split("-")[0]!;
    if ((SHIPPED as readonly string[]).includes(base)) return base;
  }
  return "en";
}

function announce() {
  version++;
  try {
    document.documentElement.lang = locale;
  } catch {}
  for (const listener of listeners) listener();
}

// For tests and the service worker: a catalog supplied directly.
export function registerCatalog(tag: string, catalog: Catalog) {
  catalogs[tag] = catalog;
}

export async function loadLocale(tag: string): Promise<void> {
  if (catalogs[tag] || !loaders[tag]) return;
  catalogs[tag] = (await loaders[tag]()).default;
}

export function setLocale(requested: string) {
  requestedTag = requested;
  const next = resolveLocale(requested);
  if (next === locale && (catalogs[next] || next === "en-XA")) return;
  if (next === "en-XA" || catalogs[next]) {
    locale = next;
    announce();
    return;
  }
  void loadLocale(next)
    .then(() => {
      // A later choice wins over one still loading.
      if (resolveLocale(requestedTag) !== next) return;
      locale = next;
      announce();
    })
    .catch(() => {});
}

export function currentLocale() {
  return locale;
}

function format(text: string, vars?: Record<string, string | number>) {
  if (!vars) return text;
  return text.replace(/\{(\w+)\}/g, (match, name: string) => (name in vars ? String(vars[name]) : match));
}

const intlTag = () => (locale === "en-XA" ? "en" : locale);

// A key with a count picks "key.one"/"key.other" by the locale's plural rules.
export function t(key: string, vars?: Record<string, string | number>, fallback?: string): string {
  let lookup = key;
  if (vars && typeof vars.count === "number") {
    const rule = new Intl.PluralRules(intlTag()).select(vars.count);
    if (`${key}.${rule}` in en) lookup = `${key}.${rule}`;
    else if (`${key}.other` in en) lookup = `${key}.other`;
  }
  const english = en[lookup] ?? fallback ?? key;
  if (locale === "en-XA") return format(pseudo(english), vars);
  const text = catalogs[locale]?.[lookup] ?? english;
  return format(text, vars);
}

export function subscribeLocale(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

// Re-renders the calling component when the language changes.
export function useLocale() {
  useSyncExternalStore(subscribeLocale, () => version, () => 0);
  return locale;
}

export function formatNumber(value: number, options?: Intl.NumberFormatOptions) {
  return new Intl.NumberFormat(intlTag(), options).format(value);
}

export function formatDate(value: number | Date, options: Intl.DateTimeFormatOptions = { dateStyle: "medium", timeStyle: "short" }) {
  return new Intl.DateTimeFormat(intlTag(), options).format(value);
}

export function formatRelative(value: number, unit: Intl.RelativeTimeFormatUnit) {
  return new Intl.RelativeTimeFormat(intlTag(), { numeric: "auto" }).format(value, unit);
}
