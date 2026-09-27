// Translation: t("key", {vars}) looks a string up in the active catalog,
// falls back to English, then to the key's own default. English is the
// complete source catalog (en.ts). "en-XA" is a pseudo-locale — every string
// accented and bracketed — that shows at a glance which text on screen has
// not been wired through t() yet.
import { useSyncExternalStore } from "react";
import en from "./en";

export type Catalog = Record<string, string>;
export interface Language {
  tag: string;
  name: string;
}

export const LANGUAGES: Language[] = [
  { tag: "", name: "Browser default" },
  { tag: "en", name: "English" },
  { tag: "en-XA", name: "Pseudo-locale (translation check)" },
];

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
let version = 0;
const listeners = new Set<() => void>();

export function resolveLocale(requested: string, browser: readonly string[] = typeof navigator === "undefined" ? [] : navigator.languages || []): string {
  const candidates = requested ? [requested] : [...browser];
  for (const tag of candidates) {
    if (tag === "en-XA") return tag;
    if (catalogs[tag]) return tag;
    const base = tag.split("-")[0]!;
    if (catalogs[base]) return base;
  }
  return "en";
}

export function setLocale(requested: string) {
  const next = resolveLocale(requested);
  if (next === locale) return;
  locale = next;
  version++;
  try {
    document.documentElement.lang = next;
  } catch {}
  for (const listener of listeners) listener();
}

export function currentLocale() {
  return locale;
}

function format(text: string, vars?: Record<string, string | number>) {
  if (!vars) return text;
  return text.replace(/\{(\w+)\}/g, (match, name: string) => (name in vars ? String(vars[name]) : match));
}

// A key with a count picks "key.one"/"key.other" by the locale's plural rules.
export function t(key: string, vars?: Record<string, string | number>, fallback?: string): string {
  let lookup = key;
  if (vars && typeof vars.count === "number") {
    const rule = new Intl.PluralRules(locale === "en-XA" ? "en" : locale).select(vars.count);
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
  return new Intl.NumberFormat(locale === "en-XA" ? "en" : locale, options).format(value);
}

export function formatDate(value: number | Date, options: Intl.DateTimeFormatOptions = { dateStyle: "medium", timeStyle: "short" }) {
  return new Intl.DateTimeFormat(locale === "en-XA" ? "en" : locale, options).format(value);
}
