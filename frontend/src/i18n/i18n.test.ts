import assert from "node:assert/strict";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";
import en, { areas as enAreas } from "./en";
import zh, { areas as zhAreas } from "./zh";
import ja, { areas as jaAreas } from "./ja";
import ko, { areas as koAreas } from "./ko";
import es, { areas as esAreas } from "./es";
import fr, { areas as frAreas } from "./fr";
import { pseudo, registerCatalog, resolveLocale, setLocale, t } from "./index";

function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return sources(path);
    return /\.tsx?$/.test(name) && !name.endsWith(".test.ts") ? [path] : [];
  });
}

test("every key the app looks up has English text", () => {
  const root = join(import.meta.dirname, "..");
  const missing = new Set<string>();
  for (const file of sources(root)) {
    const text = readFileSync(file, "utf8");
    for (const match of text.matchAll(/\bt\(\s*"([a-zA-Z0-9_.]+)"/g)) {
      const key = match[1]!;
      if (key.endsWith(".") || key === "key") continue; // a prefix completed at runtime
      // A plural key is looked up as key.one / key.other.
      if (!(key in en) && !(`${key}.other` in en)) missing.add(`${key} (${file.slice(root.length + 1)})`);
    }
  }
  assert.deepEqual([...missing], []);
});

test("placeholders fill in, and a missing key falls back sensibly", () => {
  setLocale("en");
  assert.equal(t("workspace.closeView", { label: "Build" }), "Close terminal view: Build");
  assert.equal(t("no.such.key", undefined, "Fallback"), "Fallback");
  assert.equal(t("no.such.key"), "no.such.key");
});

test("the pseudo-locale marks text but keeps placeholders working", () => {
  assert.equal(pseudo("Hi {name}"), "[Ĥí {name}]");
  setLocale("en-XA");
  assert.equal(t("workspace.closeView", { label: "Build" }), "[Çĺóšé ţéŕɱíñáĺ ṽíéŵ: Build]");
  setLocale("en");
});

test("the browser's language is the default, regional tags use their base", () => {
  assert.equal(resolveLocale("", ["xx-YY"]), "en");
  assert.equal(resolveLocale("", ["fr-CA", "en"]), "fr");
  assert.equal(resolveLocale("", ["ja_JP"]), "ja");
  assert.equal(resolveLocale("", ["zh-CN"]), "zh");
  assert.equal(resolveLocale("", ["zh-TW"]), "en");
  assert.equal(resolveLocale("en-GB"), "en");
  assert.equal(resolveLocale("ko", ["fr"]), "ko");
  assert.equal(resolveLocale("en-XA"), "en-XA");
});

const shipped = { zh: [zh, zhAreas], ja: [ja, jaAreas], ko: [ko, koAreas], es: [es, esAreas], fr: [fr, frAreas] } as const;
const placeholders = (text: string) => [...text.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort().join(",");

test("no key is defined in two English area files", () => {
  const seen = new Map<string, string>();
  const twice: string[] = [];
  for (const [area, catalog] of Object.entries(enAreas))
    for (const key of Object.keys(catalog)) {
      if (seen.has(key)) twice.push(`${key} (${seen.get(key)} and ${area})`);
      seen.set(key, area);
    }
  assert.deepEqual(twice, []);
});

for (const [tag, [catalog, areas]] of Object.entries(shipped)) {
  test(`${tag}: every English key, in the same area file, with the same placeholders`, () => {
    const problems: string[] = [];
    for (const [area, english] of Object.entries(enAreas)) {
      const translated = (areas as Record<string, Record<string, string>>)[area] || {};
      for (const [key, text] of Object.entries(english)) {
        if (!(key in translated)) problems.push(`missing ${area}: ${key}`);
        // A fragment placed before or after an inline element (code, a link)
        // may be empty where the language's word order moves its words.
        else if (!translated[key]!.trim() && !/(Before|After)$/.test(key)) problems.push(`empty ${area}: ${key}`);
        else if (placeholders(translated[key]!) !== placeholders(text)) problems.push(`placeholders ${area}: ${key}: ${translated[key]}`);
      }
      for (const key of Object.keys(translated)) if (!(key in english)) problems.push(`extra ${area}: ${key}`);
    }
    assert.deepEqual(problems.slice(0, 40), [], `${problems.length} problems`);
    assert.equal(Object.keys(catalog).length, Object.keys(en).length);
  });

  test(`${tag}: translated, not copied`, () => {
    // Product names, commands and symbols legitimately stay the same; whole
    // sentences left in English do not.
    const same = Object.entries(en).filter(([key, text]) => /[a-z]{3,} [a-z]{3,} [a-z]{3,}/i.test(text) && catalog[key] === text);
    assert.ok(same.length <= Object.keys(en).length * 0.02, `${same.length} sentences left in English, e.g. ${same.slice(0, 5).map(([key]) => key).join(", ")}`);
  });
}

test("a chosen language's text replaces English, and English fills nothing in", () => {
  registerCatalog("fr", fr);
  setLocale("fr");
  assert.equal(t("nav.board"), fr["nav.board"]);
  assert.equal(t("workspace.closeView", { label: "X" }), fr["workspace.closeView"]!.replace("{label}", "X"));
  setLocale("en");
  assert.equal(t("nav.board"), "Board");
});
